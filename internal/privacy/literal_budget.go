package privacy

import (
	"errors"
	"slices"
)

const (
	maxExactLiteralBytes   = 64 << 10
	maxLiteralPatternBytes = 8 << 20
	maxLiteralPatterns     = 200000
	// 限制自动机主要数组（含构建队列），不是整个进程或快照的堆内存限额。
	maxLiteralBuildBytes = 128 << 20
)

// ErrLiteralBudget 不含规则名称或原值，可直接用于导入及手动保存的失败提示。
var ErrLiteralBudget = errors.New("敏感值或编码词典超过编译容量上限，请减少条目或缩短值后重试")

// 在逐条派生时立即去重、检查容量，避免全部派生后才发现词典过大。
type literalPatternBuilder struct {
	patterns []string
	indexes  map[string]int
	bytes    int
}

func (b *literalPatternBuilder) add(rule *CompiledRule) error {
	if len(rule.literalPatterns) == 0 {
		return nil
	}
	if b.indexes == nil {
		b.indexes = make(map[string]int)
	}
	rule.literalIndexes = make([]int, len(rule.literalPatterns))
	for i, pattern := range rule.literalPatterns {
		idx, exists := b.indexes[pattern]
		if !exists {
			if len(pattern) > maxLiteralPatternBytes-b.bytes || len(b.patterns) >= maxLiteralPatterns {
				return ErrLiteralBudget
			}
			idx = len(b.patterns)
			b.bytes += len(pattern)
			b.patterns = append(b.patterns, pattern)
			b.indexes[pattern] = idx
		}
		// 相同词条共享字符串及自动机下标，规则身份保持独立。
		rule.literalPatterns[i] = b.patterns[idx]
		rule.literalIndexes[i] = idx
	}
	return nil
}

// 排序后的相邻词条共享最长公共前缀，能在分配状态表前准确计算 trie 节点数。
// 不改变原词条顺序，输出下标和规则优先级不受影响。
func literalStateCount(patterns []string) int {
	ordered := slices.Clone(patterns)
	slices.Sort(ordered)
	states := 1
	previous := ""
	for _, pattern := range ordered {
		prefix := 0
		for prefix < len(previous) && prefix < len(pattern) && previous[prefix] == pattern[prefix] {
			prefix++
		}
		states += len(pattern) - prefix
		previous = pattern
	}
	return states
}
