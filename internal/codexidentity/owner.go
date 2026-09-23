package codexidentity

import (
	"container/list"
	"crypto/sha256"
	"sync"
	"time"
)

// Owner 使用完整目标 scope、来源会话及策略版本限制服务端状态复用。
type Owner struct{ Scope, Session, Thread, Policy string }
type ownerEntry struct {
	digest  [32]byte
	owner   Owner
	expires time.Time
}

// OwnerIndex 有界、带 TTL 的内存索引，只保存 token 摘要；重启后未知 token 丢弃。
type OwnerIndex struct {
	mu       sync.Mutex
	capacity int
	ttl      time.Duration
	entries  map[[32]byte]*list.Element
	order    *list.List
}

func NewOwnerIndex(capacity int, ttl time.Duration) *OwnerIndex {
	if capacity < 1 {
		capacity = 4096
	}
	if ttl <= 0 {
		ttl = 30 * time.Minute
	}
	return &OwnerIndex{capacity: capacity, ttl: ttl, entries: map[[32]byte]*list.Element{}, order: list.New()}
}
func validState(token string) bool {
	return token != "" && len(token) <= maxTurnStateLength && isVisibleASCII(token, false)
}
func (x *OwnerIndex) Register(token string, owner Owner, now time.Time) {
	if x == nil || !validState(token) {
		return
	}
	x.mu.Lock()
	defer x.mu.Unlock()
	digest := sha256.Sum256([]byte(token))
	if old := x.entries[digest]; old != nil {
		// 同一个不透明值被不同 owner 返回时失去确定归属，过期前拒绝复用。
		entry := old.Value.(ownerEntry)
		if now.Before(entry.expires) && entry.owner != owner {
			owner = Owner{}
		}
		x.order.Remove(old)
	}
	e := x.order.PushBack(ownerEntry{digest, owner, now.Add(x.ttl)})
	x.entries[digest] = e
	for len(x.entries) > x.capacity {
		oldest := x.order.Front()
		delete(x.entries, oldest.Value.(ownerEntry).digest)
		x.order.Remove(oldest)
	}
}
func (x *OwnerIndex) Allows(token string, owner Owner, now time.Time) bool {
	if x == nil || !validState(token) {
		return false
	}
	x.mu.Lock()
	defer x.mu.Unlock()
	digest := sha256.Sum256([]byte(token))
	e := x.entries[digest]
	if e == nil {
		return false
	}
	entry := e.Value.(ownerEntry)
	if !now.Before(entry.expires) {
		delete(x.entries, digest)
		x.order.Remove(e)
		return false
	}
	return entry.owner != (Owner{}) && entry.owner == owner
}
