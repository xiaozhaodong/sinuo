package privacy

import (
	"encoding/binary"
	"hash/maphash"
	"sync"
)

// SegmentCache 跨请求的文本段扫描结果缓存。Claude Code / Codex 每轮都会重发整段历史，
// 绝大多数文本段与上一轮逐字节相同，命中缓存后只需扫描新增内容。
//
// 只保存 128 位哈希与命中区间偏移，不保存原文；规则集变化会产生新快照与新缓存，旧结果自然失效。
// 容量按两代淘汰：当前代写满后整体降为上一代，上一代中再次命中的条目提升回当前代。
type SegmentCache struct {
	seedA, seedB maphash.Seed
	capacity     int

	mu       sync.Mutex
	current  map[segmentKey]segmentOutcome
	previous map[segmentKey]segmentOutcome
}

// segmentKey 两个独立种子的 64 位哈希 + 长度 + 候选规则集指纹。
// 碰撞会让一段文本沿用别段的结果（可能漏脱敏），因此用 128 位哈希把概率压到可忽略。
type segmentKey struct {
	hashA, hashB uint64
	size         int
	candidates   uint64
}

const (
	// DefaultSegmentCacheCapacity 每代条目上限；单条约百余字节，两代合计约数 MB
	DefaultSegmentCacheCapacity = 16384
	// minCachedSegmentBytes 过短的文本段扫描本身比哈希加查表更便宜，不进缓存
	minCachedSegmentBytes = 256
)

// NewSegmentCache 创建文本段缓存；capacity <= 0 时使用默认容量
func NewSegmentCache(capacity int) *SegmentCache {
	if capacity <= 0 {
		capacity = DefaultSegmentCacheCapacity
	}
	return &SegmentCache{
		seedA:    maphash.MakeSeed(),
		seedB:    maphash.MakeSeed(),
		capacity: capacity,
		current:  make(map[segmentKey]segmentOutcome),
	}
}

// candidateFingerprint 同一快照下候选规则集（受 scope 影响）的指纹
func (c *SegmentCache) candidateFingerprint(candidates []int) uint64 {
	buf := make([]byte, 0, len(candidates)*4)
	for _, idx := range candidates {
		buf = binary.LittleEndian.AppendUint32(buf, uint32(idx))
	}
	return maphash.Bytes(c.seedA, buf)
}

func (c *SegmentCache) key(text string, candidates uint64) segmentKey {
	return segmentKey{
		hashA:      maphash.String(c.seedA, text),
		hashB:      maphash.String(c.seedB, text),
		size:       len(text),
		candidates: candidates,
	}
}

// lookupMany 批量查询；found[i] 为 true 时 outcomes[i] 为缓存结果。整批只加一次锁
func (c *SegmentCache) lookupMany(keys []segmentKey, outcomes []segmentOutcome, found []bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for i, key := range keys {
		if outcome, ok := c.current[key]; ok {
			outcomes[i], found[i] = outcome, true
			continue
		}
		if outcome, ok := c.previous[key]; ok {
			outcomes[i], found[i] = outcome, true
			c.putLocked(key, outcome)
		}
	}
}

// storeMany 批量写入完整扫描的结果（截断扫描的段不得写入）
func (c *SegmentCache) storeMany(keys []segmentKey, outcomes []segmentOutcome) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for i, key := range keys {
		c.putLocked(key, outcomes[i])
	}
}

func (c *SegmentCache) putLocked(key segmentKey, outcome segmentOutcome) {
	if len(c.current) >= c.capacity {
		c.previous = c.current
		c.current = make(map[segmentKey]segmentOutcome, c.capacity)
	}
	c.current[key] = outcome
}
