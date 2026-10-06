package tray

import (
	"testing"
	"time"
)

var noticeStart = time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)

func TestNotifier_OfferThrottlesAndCountsSuppressed(t *testing.T) {
	var n Notifier
	notice := Notice{ID: "failover:a", Title: "切换", Body: "改用 b"}
	if got, ok := n.Offer(notice, noticeStart); !ok || got != notice {
		t.Fatalf("第一次应原样发出: %+v %v", got, ok)
	}
	for i := 1; i <= 3; i++ {
		if _, ok := n.Offer(notice, noticeStart.Add(time.Duration(i)*time.Minute)); ok {
			t.Fatal("10 分钟内同类提示只累计次数")
		}
	}
	if _, ok := n.Offer(Notice{ID: "failover:other"}, noticeStart.Add(time.Minute)); !ok {
		t.Fatal("不同类的提示互不影响")
	}
	got, ok := n.Offer(notice, noticeStart.Add(NoticeInterval))
	if !ok || got.Body != "改用 b（自上次提醒以来共 4 次）" {
		t.Fatalf("满 10 分钟后发出并带上累计次数: %+v %v", got, ok)
	}
	if got, ok := n.Offer(notice, noticeStart.Add(2*NoticeInterval)); !ok || got.Body != "改用 b" {
		t.Fatalf("累计次数发出后清零: %+v", got)
	}
}

func TestNotifier_ProblemsOnlyNewAndRecoveryAfterAlert(t *testing.T) {
	var n Notifier
	claude := Problem{Reason: "Claude 没有可用端点", Page: "endpoints"}
	codex := Problem{Reason: "Codex 没有可调度账号", Page: "account-pool"}

	notices := n.Problems([]Problem{claude}, true, noticeStart)
	if len(notices) != 1 || notices[0].ID != "problem:"+claude.Reason || notices[0].Title != claude.Reason || notices[0].Target != "endpoints" {
		t.Fatalf("新出现的问题应提示并带上管理页: %+v", notices)
	}
	if notices := n.Problems([]Problem{claude}, true, noticeStart.Add(time.Minute)); len(notices) != 0 {
		t.Fatalf("问题持续存在不重复提示: %+v", notices)
	}
	if notices := n.Problems([]Problem{claude, codex}, true, noticeStart.Add(2*time.Minute)); len(notices) != 1 || notices[0].Title != codex.Reason {
		t.Fatalf("另一侧新出现的问题单独提示: %+v", notices)
	}
	notices = n.Problems(nil, true, noticeStart.Add(3*time.Minute))
	if len(notices) != 1 || notices[0].ID != "recovered" {
		t.Fatalf("问题全部消失且提示过时，提示一次已恢复: %+v", notices)
	}
	if notices := n.Problems(nil, true, noticeStart.Add(4*time.Minute)); len(notices) != 0 {
		t.Fatal("已恢复只提示一次")
	}
	// 同一问题 10 分钟内反复出现：被节流，也就不会再提示「已恢复」
	if notices := n.Problems([]Problem{claude}, true, noticeStart.Add(5*time.Minute)); len(notices) != 0 {
		t.Fatalf("反复出现的同一问题被节流: %+v", notices)
	}
	if notices := n.Problems(nil, true, noticeStart.Add(6*time.Minute)); len(notices) != 0 {
		t.Fatalf("没有提示过的这一轮不提示恢复: %+v", notices)
	}
}

func TestNotifier_ProblemsWhileWindowVisibleAreNotReplayed(t *testing.T) {
	var n Notifier
	claude := []Problem{{Reason: "Claude 没有可用端点"}}
	if notices := n.Problems(claude, false, noticeStart); len(notices) != 0 {
		t.Fatal("窗口可见时不提示")
	}
	if notices := n.Problems(claude, true, noticeStart.Add(time.Minute)); len(notices) != 0 {
		t.Fatal("之后隐藏窗口也不补发已经存在的问题")
	}
	if notices := n.Problems(nil, true, noticeStart.Add(2*time.Minute)); len(notices) != 0 {
		t.Fatal("没有提示过降级，恢复时也不提示")
	}
	if notices := n.Problems(claude, true, noticeStart.Add(3*time.Minute)); len(notices) != 1 {
		t.Fatalf("消失后再次出现、窗口隐藏时提示: %+v", notices)
	}
	if notices := n.Problems(nil, false, noticeStart.Add(4*time.Minute)); len(notices) != 0 {
		t.Fatal("窗口可见时恢复不提示")
	}
	if notices := n.Problems(nil, true, noticeStart.Add(5*time.Minute)); len(notices) != 0 {
		t.Fatal("恢复已在窗口可见时发生，之后不补发")
	}
}
