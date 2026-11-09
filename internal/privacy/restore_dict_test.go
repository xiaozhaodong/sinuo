package privacy

import (
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
)

func TestFunctionReplayProofEviction(t *testing.T) {
	d := NewRestoreDict()
	v := prepareDictionaryTest(t, d)
	publishDictionaryTest(t, v)
	defer v.Release()
	v.RememberFunctionReplay("shared-call", "tool", "", "first-cipher", "{}")
	v.RememberFunctionReplay("shared-call", "tool", "", "second-cipher", "{}")
	for i := 2; i <= maxFunctionReplayProofs; i++ {
		id := fmt.Sprint(i)
		v.RememberFunctionReplay(id, "tool", "", id, "{}")
	}
	if v.conflictsWithFunctionReplay("unseen", "tool", "", "first-cipher", "{}") ||
		!v.conflictsWithFunctionReplay("shared-call", "tool", "", "new-cipher", "{}") {
		t.Fatal("淘汰应移除旧密文的引用，并保留尚有记录的调用 ID")
	}
	v.RememberFunctionReplay("last-call", "tool", "", "last-cipher", "{}")
	if v.conflictsWithFunctionReplay("shared-call", "tool", "", "second-cipher", "{}") {
		t.Fatal("调用和密文均已淘汰，应允许由上游校验")
	}
}

// 测试仅使用合成值，完整占位符始终由正常 HMAC 路径动态生成。
func prepareDictionaryTest(t testing.TB, d *RestoreDict) *RestoreView {
	t.Helper()
	v, err := d.Prepare()
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func addDictionaryTest(t testing.TB, view *RestoreView, raw string) string {
	t.Helper()
	token, err := view.add(make([]byte, 32), raw)
	if err != nil {
		t.Fatal(err)
	}
	return token
}

func publishDictionaryTest(t testing.TB, view *RestoreView) {
	t.Helper()
	if err := view.Publish(); err != nil {
		t.Fatal(err)
	}
}

func TestRestoreDictionaryCrossSessionAndResidency(t *testing.T) {
	d := newRestoreDict(2, 4096)
	first := prepareDictionaryTest(t, d)
	token := addDictionaryTest(t, first, "synthetic/path/value")
	if _, ok := d.Lookup(token); ok {
		t.Fatal("放弃草稿不能发布")
	}
	publishDictionaryTest(t, first)
	first.Release()
	second := prepareDictionaryTest(t, d)
	publishDictionaryTest(t, second)
	if s, ok := second.Lookup(token); !ok || s != "synthetic/path/value" {
		t.Fatal("跨会话还原失败")
	}
	second.RecordDelivery(token, false, 2)
	second.RecordDelivery(token, true, 1)
	second.Release()
	page, err := d.Page(0, 1, func(string) string { return "••••" })
	if err != nil || page.Total != 1 || len(page.Entries) != 1 {
		t.Fatalf("字典分页异常 %+v %v", page, err)
	}
	row := page.Entries[0]
	if row.Token != token || row.MaskedValue != "••••" || row.TextCount != 2 || row.ToolCount != 1 || row.LastRestoredAt == "" {
		t.Fatal("交付统计异常")
	}
	raw, err := d.Reveal(token, row.EntryID)
	if err != nil || raw != "synthetic/path/value" {
		t.Fatal("单项原值错误")
	}

	other := prepareDictionaryTest(t, d)
	addDictionaryTest(t, other, "synthetic-second")
	publishDictionaryTest(t, other)
	other.Release()
	_, _ = d.Page(0, 1, func(s string) string { return "mask" })
	_, _ = d.Reveal(token, row.EntryID)
	third := prepareDictionaryTest(t, d)
	addDictionaryTest(t, third, "synthetic-third")
	publishDictionaryTest(t, third)
	third.Release()
	if _, ok := d.Lookup(token); ok {
		t.Fatal("页面读取不应延长驻留")
	}
	replay := prepareDictionaryTest(t, d)
	addDictionaryTest(t, replay, "synthetic/path/value")
	publishDictionaryTest(t, replay)
	defer replay.Release()
	second.RecordDelivery(token, false, 33)
	if _, err = d.Reveal(token, row.EntryID); err == nil {
		t.Fatal("旧行不能揭示新驻留值")
	}
	page, err = d.Page(0, 2, func(string) string { return "mask" })
	if err != nil || page.Entries[0].TextCount != 0 || page.Entries[0].ToolCount != 0 || page.Entries[0].EntryID == row.EntryID {
		t.Fatal("新条目应从零累计")
	}
}

func TestRestoreDictionaryAtomicPublicationAndPinnedMiss(t *testing.T) {
	d := newRestoreDict(2, 4096)
	anchor := prepareDictionaryTest(t, d)
	token := addDictionaryTest(t, anchor, "synthetic-pinned")
	publishDictionaryTest(t, anchor)
	missing := prepareDictionaryTest(t, d)
	unknown := addDictionaryTest(t, missing, "synthetic-eventual")
	if _, ok := anchor.Lookup(unknown); ok {
		t.Fatal("标识还未发布")
	}
	publishDictionaryTest(t, missing)
	missing.Release()
	if _, ok := anchor.Lookup(unknown); ok {
		t.Fatal("同一响应的落空结果必须稳定")
	}
	fail := prepareDictionaryTest(t, d)
	first := addDictionaryTest(t, fail, "synthetic-batch-a")
	last := addDictionaryTest(t, fail, "synthetic-batch-z")
	if err := fail.Publish(); err == nil {
		t.Fatal("固定条目尚在时没有足够空间")
	}
	for _, k := range []string{first, last} {
		if _, ok := d.Lookup(k); ok {
			t.Fatal("不应发布半批")
		}
	}
	if _, ok := d.Lookup(unknown); !ok {
		t.Fatal("失败写入不能先淘汰")
	}
	if got, ok := anchor.Lookup(token); !ok || got != "synthetic-pinned" {
		t.Fatal("在途条目应继续可读")
	}
	anchor.Release()
	publishDictionaryTest(t, fail)
	fail.Release()
	collision := prepareDictionaryTest(t, d)
	collision.values[first] = "conflicting synthetic value"
	if err := collision.Publish(); err == nil {
		t.Fatal("未拒绝冲突")
	}
	if got, _ := d.Lookup(first); got != "synthetic-batch-a" {
		t.Fatal("冲突改写了映射")
	}
}

func TestRestoreDictionaryConcurrentReadAndRetire(t *testing.T) {
	d := NewRestoreDict()
	first := prepareDictionaryTest(t, d)
	token := addDictionaryTest(t, first, "synthetic-value")
	publishDictionaryTest(t, first)
	defer first.Release()
	var wg sync.WaitGroup
	for i := 0; i < 48; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			v, e := d.Prepare()
			if e != nil {
				t.Error(e)
				return
			}
			if e = v.Publish(); e != nil {
				t.Error(e)
				return
			}
			defer v.Release()
			if _, ok := v.Lookup(token); !ok {
				t.Error("并发查询缺失")
				return
			}
			v.RecordDelivery(token, i%2 == 0, 1)
			if page, err := d.Page(0, 20, func(string) string { return "mask" }); err != nil || page.Total != 1 {
				t.Error("并发分页失败")
			}
		}(i)
	}
	wg.Wait()
	draft := prepareDictionaryTest(t, d)
	d.Retire()
	if err := draft.Publish(); err == nil {
		t.Fatal("停用后旧草稿不得发布")
	}
	if _, ok := first.Lookup(token); !ok {
		t.Fatal("已出站的旧流仍可收尾")
	}
	page, err := d.Page(0, 20, func(string) string { return "mask" })
	if err != nil || page.Enabled || page.Total != 0 {
		t.Fatal("停用的字典不再可见")
	}
}

func TestRestoreDictionaryGenerationSharesCapacityUntilReadersFinish(t *testing.T) {
	var shared RestoreCapacity
	old := shared.newDictionary(1, 4096)
	seed := prepareDictionaryTest(t, old)
	token := addDictionaryTest(t, seed, "synthetic old generation")
	publishDictionaryTest(t, seed)
	seed.Release()
	reader := prepareDictionaryTest(t, old)
	publishDictionaryTest(t, reader)
	old.Retire()
	newer := shared.newDictionary(1, 4096)
	insert := prepareDictionaryTest(t, newer)
	addDictionaryTest(t, insert, "synthetic new generation")
	if err := insert.Publish(); err == nil {
		t.Fatal("旧在途响应占用的载荷不能在新代际重复分配")
	}
	if raw, ok := reader.Lookup(token); !ok || raw != "synthetic old generation" {
		t.Fatal("退休后首查仍要从旧字典还原")
	}
	reader.Release()
	if len(old.entries) != 0 || old.bytes != 0 {
		t.Fatal("最后一条在途响应结束后应释放旧字典")
	}
	publishDictionaryTest(t, insert)
	insert.Release()
	newer.Retire()
	if shared.entries != 0 || shared.bytes != 0 || shared.slots != 0 {
		t.Fatalf("容量泄漏 %+v", &shared)
	}
}

// 两个同时扫描的请求均可出站。已准备的旧正文只在下一次尝试时重扫，响应交付计数不会使缓存失效。
func TestRestoreDictionaryConcurrentScanAndRetry(t *testing.T) {
	d := NewRestoreDict()
	first := prepareDictionaryTest(t, d)
	draft := prepareDictionaryTest(t, d)
	token := addDictionaryTest(t, first, "prior-synthetic-value")
	publishDictionaryTest(t, first)
	defer first.Release()
	if !first.Reusable() || draft.Reusable() {
		t.Fatal("字典新增原值时，旧扫描缓存必须失效")
	}
	publishDictionaryTest(t, draft)
	defer draft.Release()
	if draft.Reusable() {
		t.Fatal("发布时不能把旧扫描标为最新")
	}
	first.RecordDelivery(token, true, 1)
	if !first.Reusable() {
		t.Fatal("单纯的回填计数使扫描缓存失效")
	}
}

func TestRestoreIndexOverlappingPrefixes(t *testing.T) {
	words := []string{"ab", "abc", "abce", "abcd", "ababa", "ba", "界", "界面", "a"}
	patterns := make([]restorePattern, 0, len(words))
	for _, word := range words {
		patterns = append(patterns, restorePattern{value: word})
	}
	idx := newRestoreIndex(patterns)
	for _, src := range []string{"abcdabcfabc", "ababababa", "界面a界", "a", ""} {
		var got, want []string
		idx.visit(src, func(s string, _ bool, pos int) bool { got = append(got, fmt.Sprintf("%d/%s", pos, s)); return true })
		for pos := 0; pos < len(src); pos++ {
			for _, word := range words {
				if strings.HasPrefix(src[pos:], word) {
					want = append(want, fmt.Sprintf("%d/%s", pos, word))
				}
			}
		}
		slices.Sort(got)
		slices.Sort(want)
		if !slices.Equal(got, want) {
			t.Fatalf("输入 %q 的匹配位置错误: %v / %v", src, got, want)
		}
	}
}

func TestRestoreCachedSegmentRevisionAndEviction(t *testing.T) {
	d := newRestoreDict(2, 4096)
	const first, second, third = "first-value", "second-value", "third-value"
	src := strings.Repeat("ordinary ", 48) + first + " " + second + " " + third
	scan := func(want ...string) {
		t.Helper()
		v := prepareDictionaryTest(t, d)
		defer v.Release()
		for range 2 {
			var got []string
			v.index.visitSegment(src, func(value string, _ bool, _ int) bool { got = append(got, value); return true })
			slices.Sort(got)
			if !slices.Equal(got, want) {
				t.Fatalf("版本 %d: %v, 期望 %v", v.revision, got, want)
			}
		}
	}
	publish := func(value string) {
		v := prepareDictionaryTest(t, d)
		addDictionaryTest(t, v, value)
		publishDictionaryTest(t, v)
		v.Release()
	}
	scan()
	publish(first)
	scan(first)
	publish(second)
	scan(first, second)
	publish(third)
	scan(second, third)
}

func TestLargeRestoreIndexPrefixPairs(t *testing.T) {
	for _, extra := range []string{"外部", "x"} {
		// 同时覆盖双字节预筛与存在单字节模式时的完整搜索。
		patterns := []restorePattern{{value: "aba"}, {value: "ababa"}, {value: "ba"}, {value: extra}}
		for i := range 160 {
			patterns = append(patterns, restorePattern{value: fmt.Sprintf("index-%03d", i)})
		}
		idx := newRestoreIndex(patterns)
		src := "ababa " + extra + "index-159x"
		var got, want []string
		idx.visit(src, func(value string, _ bool, start int) bool {
			got = append(got, fmt.Sprintf("%d/%s", start, value))
			return true
		})
		for i := 0; i < len(src); i++ {
			for _, p := range patterns {
				if strings.HasPrefix(src[i:], p.value) {
					want = append(want, fmt.Sprintf("%d/%s", i, p.value))
				}
			}
		}
		slices.Sort(want)
		slices.Sort(got)
		if !slices.Equal(got, want) {
			t.Fatalf("前缀筛选遗漏或重复: %v / %v", got, want)
		}
	}
}
