package privacy

import (
	"fmt"
	"net/http"
	"regexp"
	"regexp/syntax"
	"sort"
	"strings"
	"time"
)

// CompiledRule 编译后的规则。literal 规则 regex 为 nil。
type CompiledRule struct {
	Rule
	regex       *regexp.Regexp
	redactGroup int
	prefilter   *rulePrefilter
	// lineScoped 命中不会跨行：只在含必含字面量的行上运行正则
	lineScoped bool
	builtin    builtinMatcher
	// literals 同一规则集的全部字面量共用的自动机；nil 时逐个 strings.Index
	literals   *literalSet
	literalIdx int
}

// Snapshot 编译后的规则快照，热路径只读。
type Snapshot struct {
	Version      int64
	Settings     Settings
	Rules        []CompiledRule
	LoadedAt     time.Time
	CompileError string
	// Cache 跨请求文本段缓存，只对本快照的规则集有效；nil 表示不缓存
	Cache *SegmentCache
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
		if parsed, err := syntax.Parse(rule.Pattern, syntax.Perl); err == nil {
			compiled.prefilter = prefilterFromSyntax(parsed)
			compiled.lineScoped = compiled.prefilter != nil && isSingleLineRegex(parsed)
		}
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
	attachLiteralSet(compiled)
	return compiled, nil
}

// attachLiteralSet 为规则集内的全部字面量规则构建共享自动机，每段文本只扫一遍
func attachLiteralSet(compiled []CompiledRule) {
	var (
		indexes  []int
		patterns []string
	)
	for i := range compiled {
		if compiled[i].MatchType == MatchTypeLiteral {
			indexes = append(indexes, i)
			patterns = append(patterns, compiled[i].Pattern)
		}
	}
	set := newLiteralSet(patterns)
	if set == nil {
		return
	}
	for k, i := range indexes {
		compiled[i].literals = set
		compiled[i].literalIdx = k
	}
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

	outcomes, policyErr := s.scanSegments(&result, candidates, segments)
	if policyErr != nil {
		result.ScanDuration = time.Since(started)
		policyErr.Result = result
		return result, policyErr
	}

	replacements := s.collectResults(&result, segments, outcomes)
	if s.Settings.Mode == ModeRedact && len(replacements) > 0 {
		result.Body = replaceSegments(body, segments, replacements)
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

// pendingSegment 未命中缓存、需要本次扫描的文本段
type pendingSegment struct {
	index     int
	key       segmentKey
	cacheable bool
	complete  bool
}

// scanSegments 扫描全部文本段，返回与 segments 对齐的结果。
// 命中缓存的段直接复用；其余段从新到旧扫描，scan_max_bytes 只约束这部分未缓存文本。
// 超出上限时按 over_limit_action 处理：scan_prefix 优先保证最新内容（最可能刚出现的敏感信息）被扫描。
func (s *Snapshot) scanSegments(result *ApplyResult, candidates []int, segments []textSegment) ([]segmentOutcome, *PolicyError) {
	outcomes := make([]segmentOutcome, len(segments))
	pending := s.resolveCachedSegments(candidates, segments, outcomes)

	limit := s.Settings.ScanMaxBytes
	if limit <= 0 {
		limit = DefaultScanMaxBytes
	}
	if s.Settings.OverLimitAction == OverLimitFailClosed && pendingBytes(pending, segments) > limit {
		return nil, s.newOverLimitError()
	}

	var scannedBytes int64
	for i := len(pending) - 1; i >= 0; i-- {
		text := segments[pending[i].index].Value
		if text == "" {
			continue
		}
		remaining := limit - scannedBytes
		if remaining <= 0 {
			result.Truncated = true
			break
		}
		pending[i].complete = int64(len(text)) <= remaining
		if !pending[i].complete {
			// 只扫描剩余预算内的前缀，其余文本透传
			text = text[:remaining]
			result.Truncated = true
		}
		scannedBytes += int64(len(text))
		outcomes[pending[i].index] = s.scanText(candidates, text)
	}
	if result.Truncated {
		result.SkippedReason = SkippedScanTruncated
	}
	s.storeScannedSegments(pending, outcomes)
	return outcomes, nil
}

// resolveCachedSegments 批量查缓存，命中的结果直接写入 outcomes，返回其余待扫描的段（文档顺序）
func (s *Snapshot) resolveCachedSegments(candidates []int, segments []textSegment, outcomes []segmentOutcome) []pendingSegment {
	pending := make([]pendingSegment, 0, len(segments))
	if s.Cache == nil {
		for i := range segments {
			pending = append(pending, pendingSegment{index: i})
		}
		return pending
	}

	fingerprint := s.Cache.candidateFingerprint(candidates)
	var (
		keys   []segmentKey
		keyed  []int
		cached []segmentOutcome
		found  []bool
	)
	for i := range segments {
		if len(segments[i].Value) >= minCachedSegmentBytes {
			keys = append(keys, s.Cache.key(segments[i].Value, fingerprint))
			keyed = append(keyed, i)
		}
	}
	if len(keys) > 0 {
		cached = make([]segmentOutcome, len(keys))
		found = make([]bool, len(keys))
		s.Cache.lookupMany(keys, cached, found)
	}

	next := 0
	for i := range segments {
		if next >= len(keyed) || keyed[next] != i {
			pending = append(pending, pendingSegment{index: i})
			continue
		}
		if found[next] {
			outcomes[i] = cached[next]
		} else {
			pending = append(pending, pendingSegment{index: i, key: keys[next], cacheable: true})
		}
		next++
	}
	return pending
}

// storeScannedSegments 只缓存完整扫描过的段；截断的段下次仍需重新扫描
func (s *Snapshot) storeScannedSegments(pending []pendingSegment, outcomes []segmentOutcome) {
	if s.Cache == nil {
		return
	}
	var (
		keys   []segmentKey
		values []segmentOutcome
	)
	for _, item := range pending {
		if item.cacheable && item.complete {
			keys = append(keys, item.key)
			values = append(values, outcomes[item.index])
		}
	}
	if len(keys) > 0 {
		s.Cache.storeMany(keys, values)
	}
}

func pendingBytes(pending []pendingSegment, segments []textSegment) int64 {
	var total int64
	for _, item := range pending {
		total += int64(len(segments[item.index].Value))
	}
	return total
}

// collectResults 按文档顺序汇总命中统计，并返回需要替换的段（段下标 -> 替换后文本）
func (s *Snapshot) collectResults(result *ApplyResult, segments []textSegment, outcomes []segmentOutcome) map[int]string {
	hits := make(map[int64]*RuleHit)
	replacements := make(map[int]string)
	for segIdx, outcome := range outcomes {
		text := segments[segIdx].Value
		s.countHits(result, hits, text, outcome.hits)
		if s.Settings.Mode == ModeRedact && len(outcome.regions) > 0 {
			replacements[segIdx] = s.applyRegions(text, outcome.regions)
		}
	}
	result.RuleHits = sortedRuleHits(hits)
	return replacements
}

func (s *Snapshot) countHits(result *ApplyResult, hits map[int64]*RuleHit, text string, spans []matchSpan) {
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
	}
}

func (s *Snapshot) newOverLimitError() *PolicyError {
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

	outcome := s.scanText(s.candidateRules(req), text)
	hits := make(map[int64]*RuleHit)
	s.countHits(&result, hits, text, outcome.hits)
	replaced := text
	if len(outcome.regions) > 0 {
		replaced = s.applyRegions(text, outcome.regions)
		result.Changed = true
	}
	result.Body = []byte(replaced)
	result.RuleHits = sortedRuleHits(hits)
	result.ScanDuration = time.Since(started)
	return result
}
