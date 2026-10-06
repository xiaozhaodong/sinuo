package tray

import (
	"strings"
	"testing"
	"time"
)

func TestRecordFailover_MergesMovesToFrontAndKeepsLimit(t *testing.T) {
	at := time.Date(2026, 10, 6, 10, 0, 0, 0, time.UTC)
	var entries []FailoverEntry
	for i, key := range []string{"a", "b", "a", "c", "d", "e", "f"} {
		entries = RecordFailover(entries, key, "切换 "+key, at.Add(time.Duration(i)*time.Minute))
	}
	input := []FailoverEntry{{Key: "x", Count: 1}}
	if RecordFailover(input, "x", "", at); input[0].Count != 1 {
		t.Fatal("不应修改入参")
	}
	var keys []string
	for _, e := range entries {
		keys = append(keys, e.Key)
	}
	if strings.Join(keys, ",") != "f,e,d,c,a" || entries[4].Count != 2 || entries[0].Count != 1 {
		t.Fatalf("新的在前、同类合并计数、最多 %d 条: %+v", RecentFailoverLimit, entries)
	}
	entries = RecordFailover(entries, "a", "切换 a", at.Add(time.Hour))
	if entries[0].Key != "a" || entries[0].Count != 3 || len(entries) != RecentFailoverLimit {
		t.Fatalf("再次发生的同类切换移到最前: %+v", entries)
	}
}

func TestMenu_RecentFailovers(t *testing.T) {
	shanghai := time.FixedZone("Asia/Shanghai", 8*3600)
	now := time.Date(2026, 10, 6, 4, 0, 0, 0, time.UTC) // 本地 12:00
	s := runningSnapshot()
	if hasItem(Build(s), "最近切换") {
		t.Fatal("没有切换时不显示")
	}
	s.Now, s.Location = now, shanghai
	s.Failovers = []FailoverEntry{
		{Key: "a", Text: "Claude 临时改用 b（原 a · 触发限流）", At: now.Add(-2 * time.Minute), Count: 3},
		{Key: "c", Text: "Codex x → y（额度已用尽）", At: now.Add(-20 * time.Hour), Count: 1},
	}
	items := Build(s)
	recent := menuItem(t, items, "最近切换")
	if items[2].Label != "最近切换" || !items[3].Separator {
		t.Fatalf("最近切换应在统计行与降级行之后、第一个分隔线之前: %+v", items[:4])
	}
	rows := recent.Children
	if rows[0].Label != "11:58  Claude 临时改用 b（原 a · 触发限流） ×3" || !rows[0].Disabled ||
		rows[1].Label != "10-05 16:00  Codex x → y（额度已用尽）" {
		t.Fatalf("按配置时区显示时间，不是当天的带日期，合并次数写在末尾: %+v", rows)
	}
	if last := rows[len(rows)-1]; last.Action != (Action{Kind: "navigate", Target: "requests"}) || last.Icon != "" {
		t.Fatalf("末尾跳请求追踪，这一级不带图标: %+v", last)
	}
}
