package privacy

import (
	"hash/maphash"
	"sync"
)

// 缓存同一字典版本下的原始命中位置。规则区间、弱值边界和签名重放策略由调用方处理。
// 每个字典至多 4096 段，每段至多 128 个区间。键不保留正文，值不保留映射或索引。
type restoreMatchCache struct {
	mu           sync.Mutex
	seedA, seedB maphash.Seed
	slots        [4096]restoreCachedSegment
}
type restoreCachedSegment struct {
	key     restoreSegmentKey
	matches []restorePosition
}
type restoreSegmentKey struct {
	hashA, hashB, revision uint64
	size                   int
}

type restorePosition struct {
	start, end int
	strong     bool
}

func newRestoreMatchCache() *restoreMatchCache {
	return &restoreMatchCache{seedA: maphash.MakeSeed(), seedB: maphash.MakeSeed()}
}
func (idx *restoreIndex) visitSegment(src string, visit func(string, bool, int) bool) bool {
	if idx == nil || len(idx.patterns) == 0 {
		return true
	}
	if idx.cache == nil || len(src) < minCachedSegmentBytes {
		return idx.visit(src, visit)
	}
	c := idx.cache
	key := restoreSegmentKey{hashA: maphash.String(c.seedA, src), hashB: maphash.String(c.seedB, src), revision: idx.revision, size: len(src)}
	slot := key.hashA % uint64(len(c.slots))
	c.mu.Lock()
	cached := c.slots[slot]
	c.mu.Unlock()
	if cached.key == key {
		for _, m := range cached.matches {
			if !visit(src[m.start:m.end], m.strong, m.start) {
				return false
			}
		}
		return true
	}
	const maxMatches = 128
	var matches []restorePosition
	complete := idx.visit(src, func(value string, strong bool, start int) bool {
		if len(matches) <= maxMatches {
			matches = append(matches, restorePosition{start, start + len(value), strong})
		}
		return visit(value, strong, start)
	})
	if complete && len(matches) <= maxMatches {
		c.mu.Lock()
		c.slots[slot] = restoreCachedSegment{key, matches}
		c.mu.Unlock()
	}
	return complete
}
