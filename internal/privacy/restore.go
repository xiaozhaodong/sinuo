package privacy

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	"math"
	"net/http"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

// RestorePrefix 是进程字典的机器标识前缀。
const RestorePrefix = "{{SNU_"
const RestoreTokenSize = len(RestorePrefix) + 32 + 2
const maxRestoreEntries = 10000
const maxRestoreBytes = 4 << 20

// 已有机器标识是不可修改的区间，只接受小写的 128 位十六进制编号。
func restoreMarkers(text string) []matchSpan {
	var spans []matchSpan
	for pos := 0; pos < len(text); {
		i := strings.Index(text[pos:], RestorePrefix)
		if i < 0 {
			break
		}
		start := pos + i
		end := start + RestoreTokenSize
		if end > len(text) {
			break
		}
		pos = start + 1
		if text[end-2:end] != "}}" {
			continue
		}
		digits := text[start+len(RestorePrefix) : end-2]
		if strings.IndexFunc(digits, func(r rune) bool { return !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') }) >= 0 {
			continue
		}
		spans = append(spans, matchSpan{start: start, end: end})
		pos = end
	}
	return spans
}

// 分类规则和补遮结果，完全包含于标识中的命中忽略，跨边界的脱敏不能安全应用。
func outsideMarkers(markers []matchSpan, span matchSpan) (bool, *PolicyError) {
	i := sort.Search(len(markers), func(i int) bool { return markers[i].end > span.start })
	if i == len(markers) || markers[i].start >= span.end {
		return true, nil
	}
	if markers[i].start <= span.start && markers[i].end >= span.end {
		return false, nil
	}
	return false, restoreError("脱敏命中跨越已有占位符边界")
}

func weakValueBoundary(text string, start, end int) bool {
	word := func(c byte) bool {
		return c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
	}
	return (start == 0 || !word(text[start-1])) && (end == len(text) || !word(text[end]))
}

// restoreRejection 只使用策略代码中的固定提示，不接收请求内容、匹配值或解析器错误。
type restoreRejection string

const codeRestoreRejected = "privacy_restore_rejected"

func restoreError(reason restoreRejection) *PolicyError {
	return &PolicyError{Code: codeRestoreRejected, StatusCode: http.StatusUnprocessableEntity, Message: string(reason), restoreReason: reason}
}

// SafeRestoreMessage 供需要清理过滤器错误的入口使用。自定义过滤器的 Message 不属于可信提示。
func (e *PolicyError) SafeRestoreMessage() string {
	if e == nil || e.Code != codeRestoreRejected {
		return ""
	}
	return string(e.restoreReason)
}

func (s *Snapshot) reversible(req Request) bool {
	if !s.Settings.ResponseRestoreEnabled || s.Settings.Mode != ModeRedact {
		return false
	}
	return (req.UpstreamType == UpstreamTypeEndpoint && isClaudeMessagesPath(req.Path)) || (req.UpstreamType == UpstreamTypeAccount && (req.Path == "/v1/responses" || req.Path == "/v1/responses/compact"))
}
func expandable(value string) bool {
	n := utf8.RuneCountInString(value)
	if n < 12 {
		return false
	}
	counts := map[rune]int{}
	for _, r := range value {
		counts[r]++
	}
	var entropy float64
	for _, c := range counts {
		p := float64(c) / float64(n)
		entropy -= p * math.Log2(p)
	}
	return entropy+1e-12 >= 3
}

func (v *RestoreView) add(key []byte, value string) (string, *PolicyError) {
	if value == "" {
		return "", restoreError("回填值为空")
	}
	h := hmac.New(sha256.New, key)
	h.Write([]byte(value))
	token := RestorePrefix + hex.EncodeToString(h.Sum(nil)[:16]) + "}}"
	if old, ok := v.values[token]; ok {
		if old != value {
			return "", restoreError("占位符冲突")
		}
		return token, nil
	}
	if len(v.values) >= maxRestoreEntries || len(value)+len(token) > maxRestoreBytes-v.size {
		return "", restoreError("请求恢复表超过上限")
	}
	v.size += len(token) + len(value)
	v.values[token] = strings.Clone(value)
	return token, nil
}

// applyReversible 复用原扫描区间，在请求内补遮裸值。扫描缓存里的切片始终只读。
func (s *Snapshot) applyReversible(req Request, body []byte, result *ApplyResult, candidates []int, segments []textSegment, view *RestoreView) *PolicyError {
	flat, compose, err := expandRestoreSegments(segments, req.Path)
	if err != nil {
		return err
	}

	outcomes, policyErr := s.scanSegments(result, candidates, flat)
	if policyErr != nil {
		return policyErr
	}
	if result.Truncated {
		return restoreError("可逆脱敏扫描不完整")
	}
	started := time.Now()
	defer func() { result.RemaskDuration = time.Since(started) }()
	discovered := map[string]bool{}
	var size int
	markers := make([][]matchSpan, len(flat))
	for i, o := range outcomes {
		markers[i] = restoreMarkers(flat[i].Value)
		for _, span := range o.discovered {
			if keep, e := outsideMarkers(markers[i], span); e != nil {
				return e
			} else if !keep {
				continue
			}
			value := flat[i].Value[span.start:span.end]
			if _, ok := discovered[value]; !ok {
				size += len(value)
				if len(discovered) >= maxRestoreEntries || size > maxRestoreBytes {
					return restoreError("请求发现值超过上限")
				}
				discovered[value] = expandable(value)
			}
		}
	}
	patterns := make([]restorePattern, 0, len(discovered))
	for value, strong := range discovered {
		patterns = append(patterns, restorePattern{value: value, strong: strong})
	}
	local := newRestoreIndex(patterns)
	replacements := make(map[int]string)
	hits := make(map[int64]*RuleHit)
	sealedWeakHits := 0
	for i, o := range outcomes {
		src := flat[i].Value
		for _, hit := range o.hits {
			if keep, _ := outsideMarkers(markers[i], hit); keep {
				s.countHits(result, hits, src, []matchSpan{hit})
			}
		}
		spans := make([]rankedSpan, 0, len(o.regions))
		for _, r := range o.regions {
			if keep, e := outsideMarkers(markers[i], r); e != nil {
				return e
			} else if keep {
				spans = append(spans, rankedSpan{matchSpan: r})
			}
		}
		var matchErr *PolicyError
		addMatch := func(value string, strong bool, start int) bool {
			r := matchSpan{start: start, end: start + len(value)}
			if !strong {
				if !weakValueBoundary(src, r.start, r.end) {
					return true
				}
				if flat[i].sealed {
					sealedWeakHits++
					return true
				}
			}
			keep, e := outsideMarkers(markers[i], r)
			if e != nil {
				matchErr = e
				return false
			}
			if keep {
				if len(spans) >= 100000 {
					matchErr = restoreError("再次遮蔽区间超过上限")
					return false
				}
				result.RemaskMatches++
				spans = append(spans, rankedSpan{matchSpan: r})
			}
			return true
		}
		if !local.visit(src, addMatch) || !view.index.visitSegment(src, func(value string, strong bool, start int) bool {
			if _, duplicate := discovered[value]; duplicate {
				return true
			}
			return addMatch(value, strong, start)
		}) {
			return matchErr
		}
		regions := mergeRedactRegions(spans)
		if len(regions) == 0 {
			continue
		}
		if flat[i].sealed {
			return restoreError("加密推理项的可见内容需要脱敏，无法原样重放")
		}
		var b strings.Builder
		pos := 0
		for _, r := range regions {
			token, e := view.add(s.RestoreKey, src[r.start:r.end])
			if e != nil {
				return e
			}
			b.WriteString(src[pos:r.start])
			b.WriteString(token)
			pos = r.end
		}
		b.WriteString(src[pos:])
		replacements[i] = b.String()
	}
	if sealedWeakHits > 0 {
		slog.Debug("推理历史原样重放", "sealed_weak_matches", sealedWeakHits)
	}
	result.RuleHits = sortedRuleHits(hits)
	if len(replacements) > 0 {
		result.Body = replaceSegments(body, segments, compose(replacements))
		result.Changed = true
	}
	return nil
}
