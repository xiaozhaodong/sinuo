package privacy

import (
	"encoding/base64"
	"net/url"
	"slices"
	"sort"
	"strings"
)

// exactLiteralPatterns 只在编译时派生已登记值的单层编码，最多 13 种含原值。
// 不解码请求，不递归派生，也不把编码形式另存为可编辑规则。
func exactLiteralPatterns(value string) []string {
	patterns := []string{value}
	add := func(pattern string) {
		if !slices.Contains(patterns, pattern) {
			patterns = append(patterns, pattern)
		}
	}
	query := url.QueryEscape(value)
	// 空用户名生成的 userinfo 以冒号开头，后面是密码段的标准转义。
	password := url.UserPassword("", value).String()[1:]
	for _, encoded := range []string{query, strings.ReplaceAll(query, "+", "%20"), url.PathEscape(value), password} {
		add(encoded)
		add(lowerPercentHex(encoded))
	}
	for _, encoding := range []*base64.Encoding{base64.StdEncoding, base64.URLEncoding} {
		encoded := encoding.EncodeToString([]byte(value))
		add(encoded)
		add(strings.TrimRight(encoded, "="))
	}
	return patterns
}

// lowerPercentHex 仅转换 %HH 的十六进制字母，原值中的大小写保持不变。
// 支持统一大写或小写的编码器输出，不枚举任意混合大小写或部分编码。
func lowerPercentHex(value string) string {
	var lowered []byte
	for i := 0; i+2 < len(value); i++ {
		if value[i] != '%' {
			continue
		}
		for j := i + 1; j <= i+2; j++ {
			if value[j] >= 'A' && value[j] <= 'F' {
				if lowered == nil {
					lowered = []byte(value)
				}
				lowered[j] = value[j] + ('a' - 'A')
			}
		}
		i += 2
	}
	if lowered == nil {
		return value
	}
	return string(lowered)
}

func findCompiledLiteralMatches(rule *CompiledRule, seg *segmentText) [][2]int {
	if len(rule.literalPatterns) == 1 {
		if rule.literals != nil {
			return nonOverlappingOccurrences(seg.literalOccurrences(rule.literals, rule.literalIndexes[0]), len(rule.Pattern))
		}
		return findLiteralMatches(rule.Pattern, seg.text)
	}
	var matches [][2]int
	for i, pattern := range rule.literalPatterns {
		if rule.literals == nil {
			matches = append(matches, findLiteralMatches(pattern, seg.text)...)
		} else {
			starts := seg.literalOccurrences(rule.literals, rule.literalIndexes[i])
			matches = append(matches, nonOverlappingOccurrences(starts, len(pattern))...)
		}
	}
	if len(rule.literalPatterns) > 1 && len(matches) > 1 {
		// 有填充 Base64 与无填充形式会在同一起点命中，统计优先取完整形式。
		// 保留不同起点的重叠区间，交由原有合并逻辑覆盖全部敏感字符。
		sort.Slice(matches, func(i, j int) bool {
			if matches[i][0] != matches[j][0] {
				return matches[i][0] < matches[j][0]
			}
			return matches[i][1] > matches[j][1]
		})
		matches = slices.Compact(matches)
	}
	return matches
}
