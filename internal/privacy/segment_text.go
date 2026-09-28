package privacy

import "strings"

// segmentText 单段文本的扫描上下文：数字串、小写副本与字面量出现位置都惰性计算，
// 同一段上的多条规则共享，避免每条规则各自全文扫描一遍。
type segmentText struct {
	text string

	digitRuns   [][2]int
	digitsReady bool

	lower      string
	lowerReady bool

	foldSpecial      bool
	foldSpecialReady bool

	literalSet    *literalSet
	literalStarts [][]int
}

// digitRunSpans 返回全部极大 ASCII 数字串区间（前后都不是数字）
func (s *segmentText) digitRunSpans() [][2]int {
	if s.digitsReady {
		return s.digitRuns
	}
	s.digitsReady = true
	text := s.text
	for i := 0; i < len(text); i++ {
		if !isASCIIDigit(text[i]) {
			continue
		}
		start := i
		for i < len(text) && isASCIIDigit(text[i]) {
			i++
		}
		s.digitRuns = append(s.digitRuns, [2]int{start, i})
	}
	return s.digitRuns
}

func (s *segmentText) asciiLower() string {
	if !s.lowerReady {
		s.lower = asciiLower(s.text)
		s.lowerReady = true
	}
	return s.lower
}

// hasNonASCIIFoldToASCII 文本是否含会大小写折叠到 ASCII 字母的非 ASCII 字符（ſ、K）
func (s *segmentText) hasNonASCIIFoldToASCII() bool {
	if !s.foldSpecialReady {
		s.foldSpecial = strings.Contains(s.text, "ſ") || strings.Contains(s.text, "K")
		s.foldSpecialReady = true
	}
	return s.foldSpecial
}

// literalOccurrences 返回字面量集合中第 idx 个字面量的全部出现起点；整组只扫一遍
func (s *segmentText) literalOccurrences(set *literalSet, idx int) []int {
	if s.literalSet != set {
		s.literalSet = set
		s.literalStarts = set.findAll(s.text)
	}
	if s.literalStarts == nil {
		return nil
	}
	return s.literalStarts[idx]
}
