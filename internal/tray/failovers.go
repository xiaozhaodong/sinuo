package tray

import (
	"fmt"
	"time"
)

// FailoverEntry 是「最近切换」的一行：通道、原目标、新目标与原因相同的切换合并计数。
type FailoverEntry struct {
	Key, Text string
	At        time.Time
	Count     int
}

// RecentFailoverLimit 是「最近切换」保留的条数；只存内存，重启清空。
const RecentFailoverLimit = 5

// RecordFailover 把一次切换并入列表（新的在前，同一 Key 合并计数并移到最前），返回新列表，不修改入参。
func RecordFailover(entries []FailoverEntry, key, text string, at time.Time) []FailoverEntry {
	count := 1
	for _, e := range entries {
		if e.Key == key {
			count = e.Count + 1
		}
	}
	next := make([]FailoverEntry, 0, RecentFailoverLimit)
	next = append(next, FailoverEntry{Key: key, Text: text, At: at, Count: count})
	for _, e := range entries {
		if e.Key != key && len(next) < RecentFailoverLimit {
			next = append(next, e)
		}
	}
	return next
}

// recentFailovers：明细只读；不是当天的切换带上日期。这一级没有图标（明细与末尾的跳转同属一级）。
func recentFailovers(s Snapshot) Item {
	location := s.Location
	if location == nil {
		location = time.Local
	}
	today := s.Now.In(location).Format("2006-01-02")
	rows := make([]Item, 0, len(s.Failovers)+2)
	for _, e := range s.Failovers {
		at := e.At.In(location)
		label := at.Format("15:04")
		if at.Format("2006-01-02") != today {
			label = at.Format("01-02 15:04")
		}
		label += "  " + e.Text
		if e.Count > 1 {
			label += fmt.Sprintf(" ×%d", e.Count)
		}
		rows = append(rows, info(label))
	}
	rows = append(rows, separator(), navigate("打开请求追踪", "requests"))
	return Item{Label: "最近切换", Icon: iconHistory, Children: rows}
}
