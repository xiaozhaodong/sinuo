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
	outputs [][]int32         // 状态 -> 在此结束的字面量下标（已合并后缀链上的输出）
	sparse  *sparseLiteralSet // 大词典按边存储，避免稠密表超限后逐条扫描文本
}

const (
	// minLiteralSetPatterns 少于该数量时逐个 strings.Index 更快（单模式走 SIMD 快速路径）
	minLiteralSetPatterns = 4
	// maxLiteralSetTableEntries 稠密转移表上限（int32 项，约 8MB）；超出时改用紧凑边表
	maxLiteralSetTableEntries = 1 << 21
)

// newLiteralSet 构建自动机；仅字面量过少或为空时返回 nil。
func newLiteralSet(patterns []string) (*literalSet, error) {
	if len(patterns) > maxLiteralPatterns {
		return nil, ErrLiteralBudget
	}
	set := &literalSet{patterns: patterns}
	totalBytes := 0
	for _, pattern := range patterns {
		if pattern == "" {
			return nil, nil
		}
		if len(pattern) > maxLiteralPatternBytes-totalBytes {
			return nil, ErrLiteralBudget
		}
		totalBytes += len(pattern)
		for i := 0; i < len(pattern); i++ {
			if set.classes[pattern[i]] == 0 {
				set.numClass++
				set.classes[pattern[i]] = set.numClass
			}
		}
	}
	if len(patterns) < minLiteralSetPatterns {
		return nil, nil
	}
	set.numClass++ // 类 0 留给未出现的字节
	states := literalStateCount(patterns)
	// 紧凑节点 16 B、边 12 B、BFS 队列 4 B，每个词条的输出链 8 B。
	if int64(states)*32+int64(len(patterns))*8 > maxLiteralBuildBytes {
		return nil, ErrLiteralBudget
	}
	entries := int64(states) * int64(set.numClass)
	// 稠密实现还复制后缀输出；按最坏的切片扩容估算，超预算使用紧凑输出链。
	denseBytes := entries*4 + int64(states)*(32+8*int64(len(patterns)))
	if entries > maxLiteralSetTableEntries || denseBytes > maxLiteralBuildBytes {
		set.buildSparse(states)
	} else {
		set.build(states)
	}
	return set, nil
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
	if set.sparse != nil {
		return set.sparse.findAll(set.patterns, text)
	}
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

// 紧凑表示仍使用同一 Aho-Corasick 匹配语义。每个 trie 状态与边占固定空间，
// 后缀命中通过 outputLink 访问，不复制后缀输出数组，不为每个节点分配 map。
type sparseLiteralNode struct{ firstEdge, fail, outputLink, firstMatch int32 }
type sparseLiteralEdge struct {
	to, next int32
	symbol   byte
}
type sparseLiteralMatch struct{ pattern, next int32 }
type sparseLiteralSet struct {
	nodes   []sparseLiteralNode
	edges   []sparseLiteralEdge
	matches []sparseLiteralMatch
	root    [256]int32
}

func (set *literalSet) buildSparse(maxStates int) {
	s := &sparseLiteralSet{
		nodes:   make([]sparseLiteralNode, 0, maxStates),
		edges:   make([]sparseLiteralEdge, 0, maxStates-1),
		matches: make([]sparseLiteralMatch, 0, len(set.patterns)),
	}
	newNode := func() int32 {
		id := int32(len(s.nodes))
		s.nodes = append(s.nodes, sparseLiteralNode{firstEdge: -1, outputLink: -1, firstMatch: -1})
		return id
	}
	newNode()
	for id, pattern := range set.patterns {
		state := int32(0)
		for i := 0; i < len(pattern); i++ {
			next := s.edge(state, pattern[i])
			if next < 0 {
				next = newNode()
				s.edges = append(s.edges, sparseLiteralEdge{to: next, next: s.nodes[state].firstEdge, symbol: pattern[i]})
				s.nodes[state].firstEdge = int32(len(s.edges) - 1)
			}
			state = next
		}
		s.matches = append(s.matches, sparseLiteralMatch{pattern: int32(id), next: s.nodes[state].firstMatch})
		s.nodes[state].firstMatch = int32(len(s.matches) - 1)
	}
	queue := make([]int32, 0, len(s.nodes))
	for i := s.nodes[0].firstEdge; i >= 0; i = s.edges[i].next {
		edge := s.edges[i]
		s.root[edge.symbol] = edge.to
		queue = append(queue, edge.to)
	}
	for head := 0; head < len(queue); head++ {
		state := queue[head]
		for i := s.nodes[state].firstEdge; i >= 0; i = s.edges[i].next {
			edge := s.edges[i]
			fallback := s.advance(s.nodes[state].fail, edge.symbol)
			s.nodes[edge.to].fail = fallback
			if s.nodes[fallback].firstMatch >= 0 {
				s.nodes[edge.to].outputLink = fallback
			} else {
				s.nodes[edge.to].outputLink = s.nodes[fallback].outputLink
			}
			queue = append(queue, edge.to)
		}
	}
	set.sparse = s
}

func (s *sparseLiteralSet) edge(state int32, symbol byte) int32 {
	for i := s.nodes[state].firstEdge; i >= 0; i = s.edges[i].next {
		if s.edges[i].symbol == symbol {
			return s.edges[i].to
		}
	}
	return -1
}

func (s *sparseLiteralSet) advance(state int32, symbol byte) int32 {
	for state != 0 {
		if next := s.edge(state, symbol); next >= 0 {
			return next
		}
		state = s.nodes[state].fail
	}
	return s.root[symbol]
}

func (s *sparseLiteralSet) findAll(patterns []string, text string) [][]int {
	var starts [][]int
	state := int32(0)
	for i := 0; i < len(text); i++ {
		state = s.advance(state, text[i])
		for output := state; output >= 0; output = s.nodes[output].outputLink {
			for match := s.nodes[output].firstMatch; match >= 0; match = s.matches[match].next {
				if starts == nil {
					starts = make([][]int, len(patterns))
				}
				id := s.matches[match].pattern
				starts[id] = append(starts[id], i+1-len(patterns[id]))
			}
		}
	}
	return starts
}
