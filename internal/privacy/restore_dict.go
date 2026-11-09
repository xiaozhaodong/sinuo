package privacy

import (
	"container/list"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	dictionaryMaxEntries    = 50000
	dictionaryMaxBytes      = 32 << 20
	maxFunctionReplayProofs = 4096
	// 一次响应按标识固定首次查询结果。进程内同时核算所有响应的引用槽。
	maxResponseLookups   = 50000
	maxActiveLookupSlots = 500000
)

var nextRestoreEntryID atomic.Uint64

type restoreEntry struct {
	token, value string
	id           uint64
	strong       bool
	pins         int
	text, tool   int64
	last         time.Time
	writeOrder   *list.Element
}

// RestoreCapacity 由服务持有，跨开关代际核算仍可供在途响应读取的所有字典。
type RestoreCapacity struct {
	mu                    sync.Mutex
	entries, bytes, slots int
}

func (b *RestoreCapacity) NewDictionary() *RestoreDict {
	return b.newDictionary(dictionaryMaxEntries, dictionaryMaxBytes)
}

func (b *RestoreCapacity) newDictionary(entries, bytes int) *RestoreDict {
	return &RestoreDict{mu: &b.mu, capacity: b, entries: make(map[string]*restoreEntry), maxEntries: entries, maxBytes: bytes}
}

// RestoreDict 是一个回填开关代际内的进程字典。单锁保护容量、引用和匹配索引的原子更新。
type RestoreDict struct {
	mu                   *sync.Mutex
	capacity             *RestoreCapacity
	retired              bool
	entries              map[string]*restoreEntry
	writes               list.List
	bytes, activeViews   int
	maxEntries, maxBytes int
	buildMu              sync.Mutex // 只串行化请求侧索引构建，响应查表使用 mu。
	matchCache           *restoreMatchCache
	index                *restoreIndex
	revision             uint64
	toolProofs           map[functionReplayProof]struct{}
	toolRefs             map[[32]byte]int
	toolOrder            []functionReplayProof
	toolCursor           int
}

func NewRestoreDict() *RestoreDict {
	return newRestoreDict(dictionaryMaxEntries, dictionaryMaxBytes)
}

func newRestoreDict(entries, bytes int) *RestoreDict {
	return new(RestoreCapacity).newDictionary(entries, bytes)
}

// Retire 阻止旧扫描发布；已发出的请求继续在这一代字典内收尾。
func (d *RestoreDict) Retire() {
	if d == nil {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.retired {
		return
	}
	d.retired = true
	d.toolProofs, d.toolRefs, d.toolOrder = nil, nil, nil
	// 退休后只保留旧响应仍可能查找的正向字典。请求侧补遮索引不再使用。
	d.index = nil
	d.matchCache = nil
	d.releaseRetiredPayloadLocked()
}

func (d *RestoreDict) releaseRetiredPayloadLocked() {
	if !d.retired || d.activeViews != 0 {
		return
	}
	d.capacity.entries -= len(d.entries)
	d.capacity.bytes -= d.bytes
	d.entries = nil
	d.bytes = 0
	d.writes.Init()
}

// RestoreView 在出站前暂存一批映射，发布后固定响应的首次查找结果。
// values 仅在独占扫描阶段写入。其余可变状态由 dict.mu 保护。
type RestoreView struct {
	values              map[string]string
	size                int
	dict                *RestoreDict
	index               *restoreIndex
	revision            uint64
	seen                map[string]*restoreEntry
	published, released bool
	lookupErr           error
	toolScope           [32]byte
}

// BindToolReplayScope 在视图交给响应处理器之前绑定请求的路由与会话；同一会话可在 HTTP/WS 间续接。
func (v *RestoreView) BindToolReplayScope(req Request) {
	v.toolScope = toolDigest([32]byte{}, req.UpstreamType, strconv.FormatInt(req.AccountID, 10), req.SessionID)
}

// 每个字段加长度前缀，避免通过改变字段边界匹配另一笔调用。缓存只保存摘要。
func toolDigest(scope [32]byte, parts ...string) [32]byte {
	h := sha256.New()
	h.Write(scope[:])
	for _, p := range parts {
		var length [8]byte
		binary.BigEndian.PutUint64(length[:], uint64(len(p)))
		h.Write(length[:])
		h.Write([]byte(p))
	}
	var digest [32]byte
	h.Sum(digest[:0])
	return digest
}

// 调用 ID 在路由和会话内定位；密文可独立识别已见的调用。字典不保留参数或密文的原值。
type functionReplayProof struct {
	call, cipher, payload [32]byte
}

func (v *RestoreView) functionProof(call, name, namespace, cipher, args string) functionReplayProof {
	return functionReplayProof{
		call:    toolDigest(v.toolScope, "call", call),
		cipher:  toolDigest([32]byte{}, "cipher", cipher),
		payload: toolDigest([32]byte{}, name, namespace, args),
	}
}

// RememberFunctionReplay 只从已解析的上游完整工具快照记录原参数。凭证随字典代际退休，容量固定。
func (v *RestoreView) RememberFunctionReplay(call, name, namespace, cipher, args string) {
	if v == nil || v.dict == nil || call == "" || name == "" || cipher == "" {
		return
	}
	proof := v.functionProof(call, name, namespace, cipher, args)
	d := v.dict
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.retired || v.released || !v.published {
		return
	}
	if _, ok := d.toolProofs[proof]; ok {
		return
	}
	if d.toolProofs == nil {
		d.toolProofs = make(map[functionReplayProof]struct{})
		d.toolRefs = make(map[[32]byte]int)
	}
	if len(d.toolOrder) < maxFunctionReplayProofs {
		d.toolOrder = append(d.toolOrder, proof)
	} else {
		old := d.toolOrder[d.toolCursor]
		delete(d.toolProofs, old)
		for _, ref := range [2][32]byte{old.call, old.cipher} {
			d.toolRefs[ref]--
			if d.toolRefs[ref] == 0 {
				delete(d.toolRefs, ref)
			}
		}
		d.toolOrder[d.toolCursor] = proof
		d.toolCursor = (d.toolCursor + 1) % maxFunctionReplayProofs
	}
	d.toolProofs[proof] = struct{}{}
	d.toolRefs[proof.call]++
	d.toolRefs[proof.cipher]++
}

// 重启、清空或淘汰后没有本地原件，按当前规则重新遮蔽，由上游校验保留的密文。
func (v *RestoreView) conflictsWithFunctionReplay(call, name, namespace, cipher, args string) bool {
	d := v.dict
	if d == nil {
		return false
	}
	proof := v.functionProof(call, name, namespace, cipher, args)
	d.mu.Lock()
	defer d.mu.Unlock()
	_, exact := d.toolProofs[proof]
	return !exact && (d.toolRefs[proof.call] > 0 || d.toolRefs[proof.cipher] > 0)
}

func (v *RestoreView) Bytes() int {
	if v == nil {
		return 0
	}
	return v.size
}

// Contains 在请求侧检查原样重放的标识，不占用响应引用，不改变淘汰顺序。
func (v *RestoreView) Contains(token string) bool {
	if v == nil {
		return false
	}
	if v.dict == nil {
		_, ok := v.values[token]
		return ok
	}
	v.dict.mu.Lock()
	defer v.dict.mu.Unlock()
	if _, ok := v.values[token]; ok {
		return true
	}
	return v.dict.entries[token] != nil
}

// Lookup 查询当前代际的映射；流式响应使用 RestoreView 固定同一标识的结果。
func (d *RestoreDict) Lookup(token string) (string, bool) {
	if d == nil {
		return "", false
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	e := d.entries[token]
	if d.retired || e == nil {
		return "", false
	}
	return e.value, true
}

func (v *RestoreView) Lookup(token string) (string, bool) {
	if v == nil {
		return "", false
	}
	d := v.dict
	if d == nil {
		value, ok := v.values[token]
		return value, ok
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if v.released {
		return "", false
	}
	if !v.published {
		if value, ok := v.values[token]; ok {
			return value, true
		}
		entry := d.entries[token]
		if entry == nil {
			return "", false
		}
		return entry.value, true
	}
	entry, seen := v.seen[token]
	if !seen {
		if len(v.seen) >= maxResponseLookups || d.capacity.slots >= maxActiveLookupSlots {
			v.lookupErr = errors.New("响应回填引用超过容量")
			return "", false
		}
		entry = d.entries[token]
		if entry != nil {
			entry.pins++
			token = entry.token
		} else {
			// 候选串可能是较大 SSE 片段的子串，仅保留本标识。
			token = strings.Clone(token)
		}
		v.seen[token] = entry
		d.capacity.slots++
	}
	if entry == nil {
		return "", false
	}
	return entry.value, true
}

func (v *RestoreView) LookupError() error {
	if v == nil || v.dict == nil {
		return nil
	}
	v.dict.mu.Lock()
	defer v.dict.mu.Unlock()
	return v.lookupErr
}

// Publish 一次性检查整批写入与所需淘汰项。失败时字典及其淘汰顺序原封不动。
func (v *RestoreView) Publish() *PolicyError {
	if v == nil || v.dict == nil {
		return nil
	}
	d := v.dict
	d.mu.Lock()
	defer d.mu.Unlock()
	if v.released {
		return restoreError("回填请求引用已释放")
	}
	if d.retired {
		return restoreError("回填设置已变更，请重发请求")
	}
	if v.published {
		return nil
	}
	if len(v.values) > maxActiveLookupSlots-d.capacity.slots {
		return restoreError("在途回填引用超过容量")
	}
	count, bytes := len(d.entries), d.bytes
	for token, value := range v.values {
		if e := d.entries[token]; e != nil {
			if e.value != value {
				return restoreError("占位符冲突")
			}
			continue
		}
		count++
		bytes += len(token) + len(value)
	}
	added := count > len(d.entries)
	var victims []*restoreEntry
	availableEntries := d.maxEntries - (d.capacity.entries - len(d.entries))
	availableBytes := d.maxBytes - (d.capacity.bytes - d.bytes)
	for p := d.writes.Back(); count > availableEntries || bytes > availableBytes; p = p.Prev() {
		if p == nil {
			return restoreError("进程回填字典容量不足，请等待在途响应完成")
		}
		e := p.Value.(*restoreEntry)
		if _, needed := v.values[e.token]; needed || e.pins > 0 {
			continue
		}
		victims = append(victims, e)
		count--
		bytes -= len(e.token) + len(e.value)
	}
	d.capacity.entries += count - len(d.entries)
	d.capacity.bytes += bytes - d.bytes
	for _, e := range victims {
		delete(d.entries, e.token)
		d.writes.Remove(e.writeOrder)
	}
	v.seen = make(map[string]*restoreEntry, len(v.values))
	// 同时扫描的其他请求可继续出站；其旧正文在下一次重试时重新扫描。
	if added || len(victims) != 0 {
		current := v.revision == d.revision
		d.revision++
		d.index = nil
		if current {
			v.revision = d.revision
		}
	}
	for token, value := range v.values {
		e := d.entries[token]
		if e == nil {
			e = &restoreEntry{token: token, value: value, strong: expandable(value), id: nextRestoreEntryID.Add(1)}
			e.writeOrder = d.writes.PushFront(e)
			d.entries[token] = e
		} else {
			d.writes.MoveToFront(e.writeOrder)
		}
		e.pins++
		v.seen[token] = e
	}
	d.bytes = bytes
	d.capacity.slots += len(v.seen)
	d.activeViews++
	v.published = true
	v.index = nil
	v.values = nil
	return nil
}

func (v *RestoreView) RecordDelivery(token string, tool bool, count int) {
	if v == nil || v.dict == nil || count <= 0 {
		return
	}
	d := v.dict
	d.mu.Lock()
	defer d.mu.Unlock()
	e := v.seen[token]
	if v.released || e == nil || d.entries[token] != e {
		return
	}
	if tool {
		e.tool += int64(count)
	} else {
		e.text += int64(count)
	}
	e.last = time.Now().UTC()
}

func (v *RestoreView) Release() {
	if v == nil || v.dict == nil {
		return
	}
	d := v.dict
	d.mu.Lock()
	defer d.mu.Unlock()
	if v.released {
		return
	}
	for _, e := range v.seen {
		if e != nil {
			e.pins--
		}
	}
	d.capacity.slots -= len(v.seen)
	v.seen = nil
	v.released = true
	if v.published {
		d.activeViews--
	}
	v.index = nil
	v.values = nil
	d.releaseRetiredPayloadLocked()
}

type RestoreDictionaryRow struct {
	Token          string `json:"token"`
	EntryID        string `json:"entry_id"`
	MaskedValue    string `json:"masked_value"`
	TextCount      int64  `json:"text_count"`
	ToolCount      int64  `json:"tool_count"`
	LastRestoredAt string `json:"last_restored_at"`
}

type RestoreDictionaryPage struct {
	Enabled bool                   `json:"enabled"`
	Total   int                    `json:"total"`
	Entries []RestoreDictionaryRow `json:"entries"`
}

// Page 返回掩码后的分页。查看操作不会固定原值、累计交付或刷新写入顺序。
func (d *RestoreDict) Page(offset, limit int, mask func(string) string) (RestoreDictionaryPage, error) {
	page := RestoreDictionaryPage{Entries: []RestoreDictionaryRow{}}
	if offset < 0 || limit <= 0 || limit > 200 {
		return page, errors.New("回填字典分页参数无效")
	}
	if d == nil {
		return page, nil
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.retired {
		return page, nil
	}
	page.Enabled = true
	page.Total = len(d.entries)
	if offset >= page.Total {
		return page, nil
	}
	for node := d.writes.Front(); node != nil && len(page.Entries) < limit; node = node.Next() {
		if offset > 0 {
			offset--
			continue
		}
		e := node.Value.(*restoreEntry)
		row := RestoreDictionaryRow{Token: e.token, EntryID: strconv.FormatUint(e.id, 10), MaskedValue: mask(e.value), TextCount: e.text, ToolCount: e.tool}
		if !e.last.IsZero() {
			row.LastRestoredAt = e.last.Format(time.RFC3339Nano)
		}
		page.Entries = append(page.Entries, row)
	}
	return page, nil
}

func (d *RestoreDict) Reveal(token, id string) (string, error) {
	if d != nil {
		d.mu.Lock()
		defer d.mu.Unlock()
		if e := d.entries[token]; !d.retired && e != nil && strconv.FormatUint(e.id, 10) == id {
			return e.value, nil
		}
	}
	return "", errors.New("这条映射已失效，请刷新字典")
}
