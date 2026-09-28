package privacy

// literalSet 多个字面量的一次扫描（Aho-Corasick），供精确敏感值与自定义字面量规则共用：
// 每段文本只扫一遍，得到每个字面量的全部出现位置（含相互重叠的出现）。
// 转移表按字节等价类压缩为稠密数组，热循环每字节一次查表。
type literalSet struct {
	patterns []string
	classes  [256]int32 // 字节 -> 等价类；0 表示不出现在任何字面量中
	numClass int32
	// trans[row+class] 为下一状态的行偏移（state*numClass）；取反减一表示该状态有输出
	trans   []int32
	outputs [][]int32 // 状态 -> 在此结束的字面量下标（已合并后缀链上的输出）
}

const (
	// minLiteralSetPatterns 少于该数量时逐个 strings.Index 更快（单模式走 SIMD 快速路径）
	minLiteralSetPatterns = 4
	// maxLiteralSetTableEntries 稠密转移表上限（int32 项，约 8MB）；超出时退回逐个查找
	maxLiteralSetTableEntries = 1 << 21
)

// newLiteralSet 构建自动机；字面量过少、为空或转移表超限时返回 nil，调用方退回逐个查找
func newLiteralSet(patterns []string) *literalSet {
	if len(patterns) < minLiteralSetPatterns {
		return nil
	}
	set := &literalSet{patterns: patterns}
	totalBytes := 0
	for _, pattern := range patterns {
		if pattern == "" {
			return nil
		}
		totalBytes += len(pattern)
		for i := 0; i < len(pattern); i++ {
			if set.classes[pattern[i]] == 0 {
				set.numClass++
				set.classes[pattern[i]] = set.numClass
			}
		}
	}
	set.numClass++ // 类 0 留给未出现的字节
	if int64(totalBytes+1)*int64(set.numClass) > maxLiteralSetTableEntries {
		return nil
	}
	set.build(totalBytes + 1)
	return set
}

// build 先建 trie，再按 BFS 补全失败转移，得到完整 DFA
func (set *literalSet) build(maxStates int) {
	nc := int(set.numClass)
	goto_ := make([]int32, nc, maxStates*nc)
	for i := range goto_ {
		goto_[i] = -1
	}
	outputs := make([][]int32, 1, maxStates)
	for id, pattern := range set.patterns {
		state := 0
		for i := 0; i < len(pattern); i++ {
			idx := state*nc + int(set.classes[pattern[i]])
			if goto_[idx] < 0 {
				goto_[idx] = int32(len(outputs))
				for range nc {
					goto_ = append(goto_, -1)
				}
				outputs = append(outputs, nil)
			}
			state = int(goto_[idx])
		}
		outputs[state] = append(outputs[state], int32(id))
	}

	fail := make([]int32, len(outputs))
	queue := make([]int32, 0, len(outputs))
	for c := range nc {
		if next := goto_[c]; next > 0 {
			queue = append(queue, next)
		} else {
			goto_[c] = 0
		}
	}
	for head := 0; head < len(queue); head++ {
		state := int(queue[head])
		for c := range nc {
			idx := state*nc + c
			fallback := goto_[int(fail[state])*nc+c]
			if next := goto_[idx]; next >= 0 {
				fail[next] = fallback
				if extra := outputs[fallback]; len(extra) > 0 {
					outputs[next] = append(outputs[next], extra...)
				}
				queue = append(queue, next)
			} else {
				goto_[idx] = fallback
			}
		}
	}

	// 目标状态编码为行偏移，有输出的状态取反减一，热循环里无需乘法和额外查表
	set.trans = goto_
	for i, next := range set.trans {
		offset := next * set.numClass
		if len(outputs[next]) > 0 {
			offset = -offset - 1
		}
		set.trans[i] = offset
	}
	set.outputs = outputs
}

// findAll 返回每个字面量在 text 中全部出现的起点（升序，可能相互重叠）；无出现时为 nil
func (set *literalSet) findAll(text string) [][]int {
	var starts [][]int
	row := int32(0)
	for i := 0; i < len(text); i++ {
		next := set.trans[row+set.classes[text[i]]]
		if next >= 0 {
			row = next
			continue
		}
		row = -next - 1
		if starts == nil {
			starts = make([][]int, len(set.patterns))
		}
		for _, id := range set.outputs[row/set.numClass] {
			starts[id] = append(starts[id], i+1-len(set.patterns[id]))
		}
	}
	return starts
}
