package privacy

import (
	"regexp/syntax"
	"strings"
	"unicode"
	"unicode/utf8"
)

// rulePrefilter 正则规则的必含字面量预过滤：任何命中都至少包含其中一个字面量，
// 文本一个都不含时直接跳过正则。Go 正则只有以固定字面量开头时才有快速路径，
// 以 \b、(?i) 或字符类开头的规则逐字节跑 NFA，预过滤让它们只在可能命中的段上运行。
type rulePrefilter struct {
	literals []string
	// fold 为 true 时 literals 已转 ASCII 小写，按大小写不敏感比较
	fold bool
}

type requiredLiteral struct {
	text string
	fold bool
}

const (
	// maxPrefilterLiterals 必含字面量集合上限，超出时不预过滤
	maxPrefilterLiterals = 16
	// maxPrefilterClassRunes 小字符类展开为单字符字面量的上限，例如 [:=：]
	maxPrefilterClassRunes = 4
)

// buildRulePrefilter 从正则语法树推导必含字面量；推导不出（例如以 .* 或大字符类为主）时返回 nil
func buildRulePrefilter(pattern string) *rulePrefilter {
	re, err := syntax.Parse(pattern, syntax.Perl)
	if err != nil {
		return nil
	}
	return prefilterFromSyntax(re)
}

func prefilterFromSyntax(re *syntax.Regexp) *rulePrefilter {
	literals, ok := requiredLiterals(re)
	if !ok || len(literals) == 0 {
		return nil
	}
	prefilter := &rulePrefilter{literals: make([]string, 0, len(literals))}
	for _, literal := range literals {
		if literal.fold {
			prefilter.fold = true
		}
	}
	seen := make(map[string]struct{}, len(literals))
	for _, literal := range literals {
		text := literal.text
		if prefilter.fold {
			// 大小写敏感的字面量转小写后只会放宽，仍是必要条件
			text = asciiLower(text)
		}
		if _, dup := seen[text]; dup {
			continue
		}
		seen[text] = struct{}{}
		prefilter.literals = append(prefilter.literals, text)
	}
	return prefilter
}

// literalCandidates 必含字面量集合；exact 为 true 表示子表达式只会匹配集合中的某个串（可继续拼接）
type literalCandidates struct {
	literals []requiredLiteral
	exact    bool
}

// requiredLiterals 返回「任何匹配都至少包含其一」的字面量集合；ok=false 表示推导不出
func requiredLiterals(re *syntax.Regexp) ([]requiredLiteral, bool) {
	candidates, ok := deriveLiterals(re)
	return candidates.literals, ok
}

func deriveLiterals(re *syntax.Regexp) (literalCandidates, bool) {
	switch re.Op {
	case syntax.OpLiteral:
		literals, ok := literalFromRunes(re.Rune, re.Flags&syntax.FoldCase != 0)
		return literalCandidates{literals: literals, exact: true}, ok
	case syntax.OpCharClass:
		literals, ok := literalsFromClass(re.Rune)
		return literalCandidates{literals: literals, exact: true}, ok
	case syntax.OpCapture:
		return deriveLiterals(re.Sub[0])
	case syntax.OpPlus:
		sub, ok := deriveLiterals(re.Sub[0])
		return literalCandidates{literals: sub.literals}, ok
	case syntax.OpRepeat:
		if re.Min < 1 {
			return literalCandidates{}, false
		}
		sub, ok := deriveLiterals(re.Sub[0])
		sub.exact = sub.exact && re.Min == 1 && re.Max == 1
		return sub, ok
	case syntax.OpConcat:
		return deriveConcatLiterals(re.Sub)
	case syntax.OpAlternate:
		union := literalCandidates{exact: true}
		for _, sub := range re.Sub {
			candidates, ok := deriveLiterals(sub)
			if !ok {
				return literalCandidates{}, false
			}
			union.literals = append(union.literals, candidates.literals...)
			union.exact = union.exact && candidates.exact
			if len(union.literals) > maxPrefilterLiterals {
				return literalCandidates{}, false
			}
		}
		return union, len(union.literals) > 0
	default:
		return literalCandidates{}, false
	}
}

// deriveConcatLiterals 串联的每一段都必须出现：相邻的精确片段拼接成更长的串（零宽断言不打断拼接），
// 再从各候选中挑选择性最好的：最短字面量越长越好，同长取数量少的
func deriveConcatLiterals(subs []*syntax.Regexp) (literalCandidates, bool) {
	var (
		best    []requiredLiteral
		run     []requiredLiteral
		allOnce = true // 全部消耗字符的片段都精确且拼成了同一段
	)
	consider := func(literals []requiredLiteral) {
		if len(literals) == 0 {
			return
		}
		if best == nil || betterLiterals(literals, best) {
			best = literals
		}
	}
	for _, sub := range subs {
		if isZeroWidth(sub.Op) {
			continue
		}
		candidates, ok := deriveLiterals(sub)
		if !ok || !candidates.exact {
			allOnce = false
			consider(run)
			run = nil
			if ok {
				consider(candidates.literals)
			}
			continue
		}
		if run == nil {
			run = candidates.literals
			continue
		}
		if joined, fits := crossLiterals(run, candidates.literals); fits {
			run = joined
			continue
		}
		allOnce = false
		consider(run)
		run = candidates.literals
	}
	if allOnce && run != nil {
		return literalCandidates{literals: run, exact: true}, true
	}
	consider(run)
	return literalCandidates{literals: best}, best != nil
}

func betterLiterals(candidate, current []requiredLiteral) bool {
	candidateMin, currentMin := minLiteralLength(candidate), minLiteralLength(current)
	if candidateMin != currentMin {
		return candidateMin > currentMin
	}
	return len(candidate) < len(current)
}

// crossLiterals 两组精确串两两拼接；结果超过上限时不拼接
func crossLiterals(left, right []requiredLiteral) ([]requiredLiteral, bool) {
	if len(left)*len(right) > maxPrefilterLiterals {
		return nil, false
	}
	joined := make([]requiredLiteral, 0, len(left)*len(right))
	for _, l := range left {
		for _, r := range right {
			joined = append(joined, requiredLiteral{text: l.text + r.text, fold: l.fold || r.fold})
		}
	}
	return joined, true
}

// isSingleLineRegex 正则不可能匹配换行符、且不含只认文本首尾的 ^ / $（\A、\z）时，
// 任何命中都落在单行之内，且行首行尾与文本首尾对断言等价，可以按行开窗匹配
func isSingleLineRegex(re *syntax.Regexp) bool {
	switch re.Op {
	case syntax.OpBeginText, syntax.OpEndText, syntax.OpAnyChar:
		return false
	case syntax.OpLiteral:
		for _, r := range re.Rune {
			if r == '\n' {
				return false
			}
		}
	case syntax.OpCharClass:
		for i := 0; i+1 < len(re.Rune); i += 2 {
			if re.Rune[i] <= '\n' && '\n' <= re.Rune[i+1] {
				return false
			}
		}
	}
	for _, sub := range re.Sub {
		if !isSingleLineRegex(sub) {
			return false
		}
	}
	return true
}

func isZeroWidth(op syntax.Op) bool {
	switch op {
	case syntax.OpEmptyMatch, syntax.OpBeginLine, syntax.OpEndLine, syntax.OpBeginText, syntax.OpEndText,
		syntax.OpWordBoundary, syntax.OpNoWordBoundary:
		return true
	default:
		return false
	}
}

func minLiteralLength(literals []requiredLiteral) int {
	shortest := len(literals[0].text)
	for _, literal := range literals[1:] {
		if len(literal.text) < shortest {
			shortest = len(literal.text)
		}
	}
	return shortest
}

// literalFromRunes 大小写不敏感的字面量只在全部字母都是 ASCII 时可用（非 ASCII 的折叠关系不做展开）；
// 语法树里折叠字面量存的是折叠环最小值（ASCII 字母为大写），统一转小写
func literalFromRunes(runes []rune, fold bool) ([]requiredLiteral, bool) {
	if len(runes) == 0 {
		return nil, false
	}
	var builder strings.Builder
	hasASCIILetter := false
	for _, r := range runes {
		if fold && r >= utf8.RuneSelf && unicode.SimpleFold(r) != r {
			return nil, false
		}
		if fold && isASCIILetter(r) {
			hasASCIILetter = true
			r = unicode.ToLower(r)
		}
		builder.WriteRune(r)
	}
	return []requiredLiteral{{text: builder.String(), fold: fold && hasASCIILetter}}, true
}

// literalsFromClass 小字符类（大小写折叠已在解析时展开为显式范围）逐字符作为候选
func literalsFromClass(ranges []rune) ([]requiredLiteral, bool) {
	count := 0
	for i := 0; i+1 < len(ranges); i += 2 {
		count += int(ranges[i+1]-ranges[i]) + 1
		if count > maxPrefilterClassRunes {
			return nil, false
		}
	}
	literals := make([]requiredLiteral, 0, count)
	for i := 0; i+1 < len(ranges); i += 2 {
		for r := ranges[i]; r <= ranges[i+1]; r++ {
			literals = append(literals, requiredLiteral{text: string(r)})
		}
	}
	return literals, len(literals) > 0
}

func isASCIILetter(r rune) bool {
	return (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')
}

// mayMatch 文本不含任何必含字面量时返回 false；无预过滤时恒为 true
func (p *rulePrefilter) mayMatch(seg *segmentText) bool {
	if p == nil {
		return true
	}
	text := seg.text
	if p.fold {
		// ſ（U+017F）与 K（U+212A）会折叠成 ASCII s / k，ASCII 小写化覆盖不到，保守放行
		if seg.hasNonASCIIFoldToASCII() {
			return true
		}
		text = seg.asciiLower()
	}
	for _, literal := range p.literals {
		if strings.Contains(text, literal) {
			return true
		}
	}
	return false
}

// asciiLower 只把 ASCII 大写字母转小写；无大写字母时原样返回，不分配
func asciiLower(text string) string {
	first := -1
	for i := 0; i < len(text); i++ {
		if c := text[i]; c >= 'A' && c <= 'Z' {
			first = i
			break
		}
	}
	if first < 0 {
		return text
	}
	lowered := []byte(text)
	for i := first; i < len(lowered); i++ {
		if c := lowered[i]; c >= 'A' && c <= 'Z' {
			lowered[i] = c + ('a' - 'A')
		}
	}
	return string(lowered)
}
