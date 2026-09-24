package privacy

import (
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"
)

// CompiledRule 编译后的规则。literal 规则 regex 为 nil。
type CompiledRule struct {
	Rule
	regex       *regexp.Regexp
	redactGroup int
	builtin     builtinMatcher
}

// Snapshot 编译后的规则快照，热路径只读。
type Snapshot struct {
	Version      int64
	Settings     Settings
	Rules        []CompiledRule
	LoadedAt     time.Time
	CompileError string
}

// CompileRule 编译单条规则（regex 规则使用 Go RE2）
func CompileRule(rule Rule) (CompiledRule, error) {
	if err := ValidateRule(rule); err != nil {
		return CompiledRule{}, err
	}
	compiled := CompiledRule{Rule: rule}
	switch rule.MatchType {
	case MatchTypeRegex:
		re, err := regexp.Compile(rule.Pattern)
		if err != nil {
			return CompiledRule{}, fmt.Errorf("compile rule %q failed: %w", rule.Name, err)
		}
		compiled.regex = re
		compiled.redactGroup = redactCaptureGroup(re)
	case MatchTypeBuiltin:
		matcher, err := compileBuiltinMatcher(rule.Pattern)
		if err != nil {
			return CompiledRule{}, fmt.Errorf("compile builtin rule %q failed: %w", rule.Name, err)
		}
		compiled.builtin = matcher
	}
	return compiled, nil
}

func redactCaptureGroup(re *regexp.Regexp) int {
	for i, name := range re.SubexpNames() {
		if i > 0 && name == "redact" {
			return i
		}
	}
	return 0
}

// CompileRules 编译规则集并按 priority 升序（同优先级按 ID）排序。
// 任一规则编译失败立即返回错误，调用方不得替换当前有效快照。
func CompileRules(rules []Rule) ([]CompiledRule, error) {
	compiled := make([]CompiledRule, 0, len(rules))
	for _, rule := range rules {
		c, err := CompileRule(rule)
		if err != nil {
			return nil, err
		}
		compiled = append(compiled, c)
	}
	sort.SliceStable(compiled, func(i, j int) bool {
		if compiled[i].Priority != compiled[j].Priority {
			return compiled[i].Priority < compiled[j].Priority
		}
		return compiled[i].ID < compiled[j].ID
	})
	return compiled, nil
}

// matchSpan 单个命中区间（解码后字符串内的字节偏移）
type matchSpan struct {
	start   int
	end     int
	ruleIdx int
}

// Apply 对请求 body 执行隐私过滤。
// 返回的 error 只会是 *PolicyError（fail_closed 短路）；
// 其余内部错误按 Settings.OnError 处理：fail_open 时记录跳过原因并透传原 body。
func (s *Snapshot) Apply(req Request, body []byte) (ApplyResult, error) {
	result := ApplyResult{Body: body, Action: ModeDisabled}
	if s == nil {
		return result, nil
	}
	result.SnapshotVersion = s.Version
	if s.Settings.Mode == ModeDisabled || s.Settings.Mode == "" {
		return result, nil
	}
	result.Action = s.Settings.Mode

	started := time.Now()
	candidates := s.candidateRules(req)
	if len(candidates) == 0 {
		result.ScanDuration = time.Since(started)
		return result, nil
	}

	if len(body) == 0 {
		result.SkippedReason = SkippedEmptyBody
		result.ScanDuration = time.Since(started)
		return result, nil
	}

	segments, skippedReason, err := s.collectSegments(req, body)
	if err != nil {
		return s.handleScanError(result, started, err)
	}
	if skippedReason != "" {
		result.SkippedReason = skippedReason
		result.ScanDuration = time.Since(started)
		return result, nil
	}
	if len(segments) == 0 {
		result.ScanDuration = time.Since(started)
		return result, nil
	}

	scanned, policyErr := s.scanSegments(req, &result, candidates, segments)
	if policyErr != nil {
		result.ScanDuration = time.Since(started)
		policyErr.Result = result
		return result, policyErr
	}

	if s.Settings.Mode == ModeRedact && len(scanned) > 0 {
		result.Body = replaceSegments(body, segments, scanned)
		result.Changed = true
	}
	result.ScanDuration = time.Since(started)
	return result, nil
}

// candidateRules 按 scope 预过滤启用规则
func (s *Snapshot) candidateRules(req Request) []int {
	candidates := make([]int, 0, len(s.Rules))
	for i := range s.Rules {
		rule := &s.Rules[i]
		if !rule.Enabled {
			continue
		}
		if rule.Scope.Matches(req) {
			candidates = append(candidates, i)
		}
	}
	return candidates
}

// collectSegments 按请求路径/内容类型收集可扫描文本段
func (s *Snapshot) collectSegments(req Request, body []byte) ([]textSegment, string, error) {
	if matcher := walkerForPath(req.Path); matcher != nil {
		segments, err := collectJSONTextSegments(body, matcher)
		if err != nil {
			return nil, "", err
		}
		if isClaudeMessagesPath(req.Path) {
			segments = dropClaudeAttributionSegments(segments)
		}
		return segments, "", nil
	}
	contentType := strings.ToLower(strings.TrimSpace(req.ContentType))
	if strings.HasPrefix(contentType, "text/") {
		return []textSegment{{Start: 0, End: len(body), Value: string(body), IsJSON: false}}, "", nil
	}
	if strings.Contains(contentType, "json") {
		// 未知 JSON path 默认不扫描，避免隐藏匹配
		return nil, SkippedUnsupportedPath, nil
	}
	return nil, SkippedNonTextBody, nil
}

// scanSegments 对文本段应用候选规则，返回需要替换的段（段下标 -> 替换后文本）。
// 超出 scan_max_bytes 时按 over_limit_action 处理。
func (s *Snapshot) scanSegments(req Request, result *ApplyResult, candidates []int, segments []textSegment) (map[int]string, *PolicyError) {
	limit := s.Settings.ScanMaxBytes
	if limit <= 0 {
		limit = DefaultScanMaxBytes
	}

	hits := make(map[int64]*RuleHit)
	replacements := make(map[int]string)
	var scannedBytes int64

	for segIdx, seg := range segments {
		if result.Truncated {
			break
		}
		text := seg.Value
		remaining := limit - scannedBytes
		if remaining <= 0 {
			if s.Settings.OverLimitAction == OverLimitFailClosed {
				return nil, s.newOverLimitError(req)
			}
			result.Truncated = true
			result.SkippedReason = SkippedScanTruncated
			break
		}
		if int64(len(text)) > remaining {
			if s.Settings.OverLimitAction == OverLimitFailClosed {
				return nil, s.newOverLimitError(req)
			}
			// 只扫描剩余预算内的前缀，其余文本透传
			text = text[:remaining]
			result.Truncated = true
			result.SkippedReason = SkippedScanTruncated
		}
		scannedBytes += int64(len(text))

		spans := s.collectMatchSpans(candidates, text)
		if len(spans) == 0 {
			continue
		}

		var redactSpans []matchSpan
		for _, span := range spans {
			rule := &s.Rules[span.ruleIdx]
			hit := hits[rule.ID]
			if hit == nil {
				hit = &RuleHit{RuleID: rule.ID, RuleName: rule.Name, Source: rule.Source, Action: rule.Action}
				hits[rule.ID] = hit
			}
			hit.Count++
			hit.Matches = append(hit.Matches, text[span.start:span.end])
			result.HitCount++
			if s.Settings.Mode == ModeRedact && rule.Action == ActionRedact {
				redactSpans = append(redactSpans, span)
			}
		}
		if len(redactSpans) > 0 {
			replacements[segIdx] = s.applySpans(seg.Value, redactSpans)
		}
	}

	result.RuleHits = sortedRuleHits(hits)
	return replacements, nil
}

// collectMatchSpans 按规则优先级收集命中区间；与更高优先级规则重叠的命中被丢弃。
func (s *Snapshot) collectMatchSpans(candidates []int, text string) []matchSpan {
	var accepted []matchSpan
	for _, ruleIdx := range candidates {
		rule := &s.Rules[ruleIdx]
		for _, loc := range findRuleMatches(rule, text) {
			span := matchSpan{start: loc[0], end: loc[1], ruleIdx: ruleIdx}
			if overlapsAny(accepted, span) {
				continue
			}
			accepted = append(accepted, span)
		}
	}
	sort.Slice(accepted, func(i, j int) bool { return accepted[i].start < accepted[j].start })
	return accepted
}

// findRuleMatches 返回规则在文本中的所有命中区间（字节偏移）
func findRuleMatches(rule *CompiledRule, text string) [][2]int {
	if rule.builtin != nil {
		return rule.builtin(text)
	}
	if rule.regex != nil {
		if rule.redactGroup > 0 {
			locs := rule.regex.FindAllStringSubmatchIndex(text, -1)
			out := make([][2]int, 0, len(locs))
			for _, loc := range locs {
				groupStart := rule.redactGroup * 2
				groupEnd := groupStart + 1
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
	pattern := rule.Pattern
	if pattern == "" {
		return nil
	}
	var out [][2]int
	offset := 0
	for {
		idx := strings.Index(text[offset:], pattern)
		if idx < 0 {
			break
		}
		start := offset + idx
		out = append(out, [2]int{start, start + len(pattern)})
		offset = start + len(pattern)
	}
	return out
}

func overlapsAny(spans []matchSpan, candidate matchSpan) bool {
	for _, span := range spans {
		if candidate.start < span.end && span.start < candidate.end {
			return true
		}
	}
	return false
}

// applySpans 将命中区间替换为对应规则的占位符
func (s *Snapshot) applySpans(text string, spans []matchSpan) string {
	sort.Slice(spans, func(i, j int) bool { return spans[i].start < spans[j].start })
	var builder strings.Builder
	builder.Grow(len(text))
	cursor := 0
	for _, span := range spans {
		builder.WriteString(text[cursor:span.start])
		placeholder := s.Rules[span.ruleIdx].Placeholder
		if placeholder == "" {
			placeholder = DefaultPlaceholder
		}
		builder.WriteString(placeholder)
		cursor = span.end
	}
	builder.WriteString(text[cursor:])
	return builder.String()
}

func (s *Snapshot) newOverLimitError(req Request) *PolicyError {
	return &PolicyError{
		StatusCode: http.StatusRequestEntityTooLarge,
		Code:       CodeScanBodyTooLarge,
		Message: fmt.Sprintf("scannable text exceeds scan_max_bytes (%d) and over_limit_action is fail_closed",
			s.Settings.ScanMaxBytes),
	}
}

// handleScanError 按 on_error 策略处理内部扫描错误（不含命中原文）
func (s *Snapshot) handleScanError(result ApplyResult, started time.Time, err error) (ApplyResult, error) {
	result.ScanDuration = time.Since(started)
	if s.Settings.OnError == OnErrorFailClosed {
		policyErr := &PolicyError{
			StatusCode: http.StatusUnprocessableEntity,
			Code:       CodeScanFailed,
			Message:    fmt.Sprintf("privacy scan failed and on_error is fail_closed: %v", err),
		}
		result.SkippedReason = CodeScanFailed
		policyErr.Result = result
		return result, policyErr
	}
	result.SkippedReason = fmt.Sprintf("scan_error: %v", err)
	return result, nil
}

func sortedRuleHits(hits map[int64]*RuleHit) []RuleHit {
	if len(hits) == 0 {
		return nil
	}
	out := make([]RuleHit, 0, len(hits))
	for _, hit := range hits {
		out = append(out, *hit)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].RuleID < out[j].RuleID })
	return out
}

// ApplyToText 对一段裸文本执行规则（测试面板用）。
// 始终按 redact 语义返回替换后文本，但不会越过 mode=disabled 的快照。
func (s *Snapshot) ApplyToText(req Request, text string) ApplyResult {
	result := ApplyResult{Action: ModeRedact}
	if s == nil {
		return result
	}
	result.SnapshotVersion = s.Version
	started := time.Now()

	candidates := s.candidateRules(req)
	hits := make(map[int64]*RuleHit)
	var redactSpans []matchSpan
	spans := s.collectMatchSpans(candidates, text)
	for _, span := range spans {
		rule := &s.Rules[span.ruleIdx]
		hit := hits[rule.ID]
		if hit == nil {
			hit = &RuleHit{RuleID: rule.ID, RuleName: rule.Name, Source: rule.Source, Action: rule.Action}
			hits[rule.ID] = hit
		}
		hit.Count++
		hit.Matches = append(hit.Matches, text[span.start:span.end])
		result.HitCount++
		if rule.Action == ActionRedact {
			redactSpans = append(redactSpans, span)
		}
	}
	replaced := text
	if len(redactSpans) > 0 {
		replaced = s.applySpans(text, redactSpans)
		result.Changed = true
	}
	result.Body = []byte(replaced)
	result.RuleHits = sortedRuleHits(hits)
	result.ScanDuration = time.Since(started)
	return result
}
