package privacy

import (
	"sort"
	"strings"
	"unicode/utf8"
)

// 索引复用字典原值。每个原值只占一个节点，不随原值字节数分配状态表。
// 同一版本的请求共享只读索引；响应和重试缓存不持有整个索引。
type restorePattern struct {
	value  string
	parent int // 按字典序在它前面的最长真前缀，无则为 -1
	strong bool
}

type restoreIndex struct {
	patterns    []restorePattern
	cache       *restoreMatchCache
	revision    uint64
	prefixPairs []uint64 // 大字典先按相邻两字节筛选，再搜索同首字节区间。
	starts      []int    // 按首字节划分的连续区间
}

func newRestoreIndex(patterns []restorePattern) *restoreIndex {
	if len(patterns) == 0 {
		return nil
	}
	idx := &restoreIndex{patterns: patterns}
	if len(patterns) < minLiteralSetPatterns {
		return idx
	}
	idx.starts = make([]int, 257)
	if len(patterns) >= 128 {
		idx.prefixPairs = make([]uint64, 1<<10)
		for _, p := range patterns {
			if len(p.value) < 2 {
				idx.prefixPairs = nil
				break
			}
			pair := int(p.value[0])<<8 | int(p.value[1])
			idx.prefixPairs[pair>>6] |= 1 << uint(pair&63)
		}
	}
	for i, pattern := range patterns {
		idx.starts[int(pattern.value[0])+1]++
		patterns[i].parent = -1
	}
	for i := 1; i < len(idx.starts); i++ {
		idx.starts[i] += idx.starts[i-1]
	}
	p := idx.patterns
	sort.Slice(p, func(i, j int) bool { return p[i].value < p[j].value })
	for i := 1; i < len(p); i++ {
		parent := i - 1
		for parent >= 0 && !strings.HasPrefix(p[i].value, p[parent].value) {
			parent = p[parent].parent
		}
		p[i].parent = parent
	}
	return idx
}

// 同首字节的模式按字典序排列。若某模式匹配当前位置，它一定是字典序前驱
// 或该前驱的前缀。枚举这些前缀可以保留嵌套和重叠命中。
func (idx *restoreIndex) visit(text string, match func(string, bool, int) bool) bool {
	if idx == nil {
		return true
	}
	p := idx.patterns
	if len(p) < minLiteralSetPatterns {
		for _, pattern := range p {
			for pos := 0; pos < len(text); {
				i := strings.Index(text[pos:], pattern.value)
				if i < 0 {
					break
				}
				pos += i
				if !match(pattern.value, pattern.strong, pos) {
					return false
				}
				pos++
			}
		}
		return true
	}
	for start := 0; start < len(text); start++ {
		b := int(text[start])
		if idx.prefixPairs != nil {
			if start+1 == len(text) {
				break
			}
			pair := b<<8 | int(text[start+1])
			if idx.prefixPairs[pair>>6]&(1<<uint(pair&63)) == 0 {
				continue
			}
		}
		lo, hi := idx.starts[b], idx.starts[b+1]
		if lo == hi {
			continue
		}
		tail := text[start:]
		end := lo + sort.Search(hi-lo, func(i int) bool { return p[lo+i].value > tail })
		for i := end - 1; i >= lo; i = p[i].parent {
			if strings.HasPrefix(tail, p[i].value) && !match(p[i].value, p[i].strong, start) {
				return false
			}
		}
	}
	return true
}

func (d *RestoreDict) Prepare() (*RestoreView, *PolicyError) {
	v := &RestoreView{dict: d, values: make(map[string]string)}
	if d == nil {
		return v, nil
	}
	d.mu.Lock()
	if d.retired {
		d.mu.Unlock()
		return nil, restoreError("回填设置已变更，请重发请求")
	}
	v.index, v.revision = d.index, d.revision
	ready := d.index != nil || len(d.entries) == 0
	d.mu.Unlock()
	if ready {
		return v, nil
	}

	d.buildMu.Lock()
	defer d.buildMu.Unlock()
	d.mu.Lock()
	if d.retired {
		d.mu.Unlock()
		return nil, restoreError("回填设置已变更，请重发请求")
	}
	v.index, v.revision = d.index, d.revision
	if v.index != nil || len(d.entries) == 0 {
		d.mu.Unlock()
		return v, nil
	}
	patterns := make([]restorePattern, 0, len(d.entries))
	for _, e := range d.entries {
		patterns = append(patterns, restorePattern{value: e.value, strong: e.strong})
	}
	if d.matchCache == nil {
		d.matchCache = newRestoreMatchCache()
	}
	cache := d.matchCache
	d.mu.Unlock()

	// 短弱值仅在发现它的请求中扩展，正向字典仍可用于响应回填。
	eligible := patterns[:0]
	for _, p := range patterns {
		if p.strong || utf8.RuneCountInString(p.value) >= 6 {
			eligible = append(eligible, p)
		}
	}
	clear(patterns[len(eligible):])
	idx := newRestoreIndex(eligible)
	if idx == nil {
		idx = &restoreIndex{}
	}
	idx.cache = cache
	idx.revision = v.revision
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.retired {
		return nil, restoreError("回填设置已变更，请重发请求")
	}
	if d.revision == v.revision {
		d.index = idx
	}
	// 并发发布期间完成的旧索引只供本次扫描，重试仍须核对版本。
	v.index = idx
	return v, nil
}

// 当前尝试按扫描开始时的字典处理；重试复用正文前核对版本。
func (v *RestoreView) Reusable() bool {
	if v == nil || v.dict == nil {
		return true
	}
	d := v.dict
	d.mu.Lock()
	defer d.mu.Unlock()
	return !v.released && !d.retired && v.revision == d.revision
}
