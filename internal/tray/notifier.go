package tray

import (
	"fmt"
	"time"
)

// NoticeInterval：同一类提示的最短间隔；期间再次发生只累计次数，下次发出时写进正文。
const NoticeInterval = 10 * time.Minute

// Notice 是一条系统通知。ID 相同的通知会被系统替换，通知中心不会堆积；Target 是点击后打开的页面。
type Notice struct {
	ID, Title, Body, Target string
}

type noticeRecord struct {
	sentAt     time.Time
	suppressed int
}

// Notifier 决定哪些事件发系统通知：同类事件节流合并；降级只在新出现时提示，
// 恢复只在这一轮确实提示过降级后才提示。时间由调用方传入；非并发安全，由调用方加锁。
type Notifier struct {
	records  map[string]*noticeRecord
	problems map[string]bool // 上次判定时存在的问题（按原因）
	alerted  bool            // 这一轮降级是否已经发过通知
}

// Offer 按 ID 节流：距上次发出不足 NoticeInterval 时只累计次数，返回 false。
func (n *Notifier) Offer(notice Notice, now time.Time) (Notice, bool) {
	if n.records == nil {
		n.records = map[string]*noticeRecord{}
	}
	record := n.records[notice.ID]
	if record != nil && now.Sub(record.sentAt) < NoticeInterval {
		record.suppressed++
		return Notice{}, false
	}
	if record != nil && record.suppressed > 0 {
		notice.Body += fmt.Sprintf("（自上次提醒以来共 %d 次）", record.suppressed+1)
	}
	n.records[notice.ID] = &noticeRecord{sentAt: now}
	return notice, true
}

// Problems 比对本次与上次判定的问题：新出现的逐条提示（同样节流）；问题全部消失、
// 且这一轮提示过时，提示一次「已恢复」。deliver 为 false（窗口可见、通知关闭）时只更新状态，
// 之后窗口隐藏也不补发这一轮已经存在的问题。
func (n *Notifier) Problems(problems []Problem, deliver bool, now time.Time) []Notice {
	current := make(map[string]bool, len(problems))
	var notices []Notice
	for _, p := range problems {
		current[p.Reason] = true
		if n.problems[p.Reason] || !deliver {
			continue
		}
		notice := Notice{ID: "problem:" + p.Reason, Title: p.Reason, Target: p.Page,
			Body: "这类请求现在会失败。可在菜单栏菜单里就地处理，或点击查看。"}
		if sent, ok := n.Offer(notice, now); ok {
			notices = append(notices, sent)
			n.alerted = true
		}
	}
	if len(current) == 0 {
		if n.alerted && deliver {
			notices = append(notices, Notice{ID: "recovered", Title: "已恢复", Body: "之前提示的问题已消失，请求可以正常转发。"})
		}
		n.alerted = false
	}
	n.problems = current
	return notices
}
