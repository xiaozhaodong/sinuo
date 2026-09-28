package privacy

import (
	"sort"
	"strings"
)

// matchSpan 单个命中区间（解码后字符串内的字节偏移）
type matchSpan struct {
	start   int
	end     int
	ruleIdx int
}

// segmentOutcome 一段文本的扫描结果，只含偏移与规则下标（可安全缓存，不含原文）。
// hits 按优先级去重，用于命中统计；regions 为脱敏区域，互不重叠且按起点升序。
type segmentOutcome struct {
	hits    []matchSpan
	regions []matchSpan
}

type rankedSpan struct {
	matchSpan
	rank int
}

// scanText 对一段文本按优先级应用候选规则。
// 命中统计沿用「高优先级先占位，与之重叠的低优先级命中不计」；
// 脱敏区域取全部 redact 规则命中的并集，占位符取区域内优先级最高的 redact 规则。
// 这样低优先级命中不会因为部分重叠被整段丢弃，也不会被重叠的 detect 规则挡住而漏脱敏。
func (s *Snapshot) scanText(candidates []int, text string) segmentOutcome {
	seg := segmentText{text: text}
	var (
		outcome segmentOutcome
		redact  []rankedSpan
	)
	for rank, ruleIdx := range candidates {
		rule := &s.Rules[ruleIdx]
		for _, loc := range findRuleMatches(rule, &seg) {
			span := matchSpan{start: loc[0], end: loc[1], ruleIdx: ruleIdx}
			outcome.hits = insertNonOverlapping(outcome.hits, span)
			if rule.Action == ActionRedact {
				redact = append(redact, rankedSpan{matchSpan: span, rank: rank})
			}
		}
	}
	outcome.regions = mergeRedactRegions(redact)
	return outcome
}

// insertNonOverlapping 向按起点升序、互不重叠的区间表插入 span；与已有区间重叠时丢弃
func insertNonOverlapping(spans []matchSpan, span matchSpan) []matchSpan {
	i := sort.Search(len(spans), func(i int) bool { return spans[i].end > span.start })
	if i < len(spans) && spans[i].start < span.end {
		return spans
	}
	spans = append(spans, matchSpan{})
	copy(spans[i+1:], spans[i:])
	spans[i] = span
	return spans
}

// mergeRedactRegions 合并相互重叠的 redact 命中；首尾相接的命中保持独立占位符
func mergeRedactRegions(spans []rankedSpan) []matchSpan {
	if len(spans) == 0 {
		return nil
	}
	sort.Slice(spans, func(i, j int) bool {
		if spans[i].start != spans[j].start {
			return spans[i].start < spans[j].start
		}
		return spans[i].rank < spans[j].rank
	})
	regions := make([]matchSpan, 0, len(spans))
	current := spans[0]
	for _, span := range spans[1:] {
		if span.start >= current.end {
			regions = append(regions, current.matchSpan)
			current = span
			continue
		}
		current.end = max(current.end, span.end)
		if span.rank < current.rank {
			current.rank, current.ruleIdx = span.rank, span.ruleIdx
		}
	}
	return append(regions, current.matchSpan)
}

// findRuleMatches 返回规则在文本中的所有命中区间（字节偏移）
func findRuleMatches(rule *CompiledRule, seg *segmentText) [][2]int {
	switch {
	case rule.builtin != nil:
		return rule.builtin(seg)
	case rule.regex != nil:
		if !rule.prefilter.mayMatch(seg) {
			return nil
		}
		if rule.lineScoped {
			return findRegexMatchesInLiteralLines(rule, seg)
		}
		return findRegexMatches(rule, seg.text)
	case rule.literals != nil:
		return nonOverlappingOccurrences(seg.literalOccurrences(rule.literals, rule.literalIdx), len(rule.Pattern))
	default:
		return findLiteralMatches(rule.Pattern, seg.text)
	}
}

func findRegexMatches(rule *CompiledRule, text string) [][2]int {
	if rule.redactGroup > 0 {
		locs := rule.regex.FindAllStringSubmatchIndex(text, -1)
		out := make([][2]int, 0, len(locs))
		groupStart := rule.redactGroup * 2
		groupEnd := groupStart + 1
		for _, loc := range locs {
			if len(loc) <= groupEnd {
				continue
			}
			if loc[groupStart] >= 0 && loc[groupEnd] > loc[groupStart] {
				out = append(out, [2]int{loc[groupStart], loc[groupEnd]})
			}
		}
		return out
	}
	locs := rule.regex.FindAllStringIndex(text, -1)
	out := make([][2]int, 0, len(locs))
	for _, loc := range locs {
		if loc[1] > loc[0] { // 跳过空匹配
			out = append(out, [2]int{loc[0], loc[1]})
		}
	}
	return out
}

// findRegexMatchesInLiteralLines 命中不跨行的规则只在含必含字面量的行上运行正则。
// 每个命中都含某个字面量且落在单行内，所以这些行覆盖了全部命中；
// 行首行尾对 \b 与 (?m)^$ 的判定与在全文中相同，结果与全文扫描一致。
func findRegexMatchesInLiteralLines(rule *CompiledRule, seg *segmentText) [][2]int {
	text := seg.text
	haystack := text
	if rule.prefilter.fold {
		if seg.hasNonASCIIFoldToASCII() {
			return findRegexMatches(rule, text)
		}
		haystack = seg.asciiLower() // ASCII 小写化不改变字节偏移
	}
	var lines [][2]int
	for _, literal := range rule.prefilter.literals {
		for offset := 0; offset < len(haystack); {
			idx := strings.Index(haystack[offset:], literal)
			if idx < 0 {
				break
			}
			pos := offset + idx
			line := [2]int{strings.LastIndexByte(text[:pos], '\n') + 1, len(text)}
			if end := strings.IndexByte(text[pos:], '\n'); end >= 0 {
				line[1] = pos + end
			}
			lines = append(lines, line)
			offset = line[1] + 1
		}
	}
	sort.Slice(lines, func(i, j int) bool { return lines[i][0] < lines[j][0] })
	var out [][2]int
	for i, line := range lines {
		if i > 0 && line[0] == lines[i-1][0] {
			continue
		}
		for _, loc := range findRegexMatches(rule, text[line[0]:line[1]]) {
			out = append(out, [2]int{loc[0] + line[0], loc[1] + line[0]})
		}
	}
	return out
}

// findLiteralMatches 从左到右不重叠地查找字面量（与 strings.Index 逐次推进等价）
func findLiteralMatches(pattern, text string) [][2]int {
	if pattern == "" {
		return nil
	}
	var out [][2]int
	offset := 0
	for {
		idx := strings.Index(text[offset:], pattern)
		if idx < 0 {
			return out
		}
		start := offset + idx
		out = append(out, [2]int{start, start + len(pattern)})
		offset = start + len(pattern)
	}
}

// nonOverlappingOccurrences 把全部出现位置（可能重叠）收敛为与 findLiteralMatches 相同的从左到右不重叠结果
func nonOverlappingOccurrences(starts []int, length int) [][2]int {
	var out [][2]int
	next := 0
	for _, start := range starts {
		if start < next {
			continue
		}
		out = append(out, [2]int{start, start + length})
		next = start + length
	}
	return out
}

// applyRegions 将脱敏区域替换为对应规则的占位符
func (s *Snapshot) applyRegions(text string, regions []matchSpan) string {
	var builder strings.Builder
	builder.Grow(len(text))
	cursor := 0
	for _, region := range regions {
		builder.WriteString(text[cursor:region.start])
		placeholder := s.Rules[region.ruleIdx].Placeholder
		if placeholder == "" {
			placeholder = DefaultPlaceholder
		}
		builder.WriteString(placeholder)
		cursor = region.end
	}
	builder.WriteString(text[cursor:])
	return builder.String()
}
