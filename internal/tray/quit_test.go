package tray

import "testing"

func TestQuit_NoRequestsAndPowerOff(t *testing.T) {
	for _, tc := range []struct {
		active int
		power  bool
	}{{0, false}, {8, true}} {
		q := &QuitState{}
		allow, prompt := q.Request(tc.active, tc.power)
		if !allow || prompt {
			t.Fatalf("应直接退出: %+v", tc)
		}
	}
}

func TestQuit_ConfirmationChoices(t *testing.T) {
	for _, choice := range []QuitChoice{QuitWait, QuitNow, QuitCancel} {
		q := &QuitState{}
		allow, prompt := q.Request(2, false)
		if allow || !prompt {
			t.Fatal("有请求必须确认")
		}
		if _, duplicate := q.Request(2, false); duplicate {
			t.Fatal("不能重复弹框")
		}
		if got := q.Choose(choice, 2); got != (choice == QuitNow) {
			t.Fatalf("选择 %v 退出=%v", choice, got)
		}
		if q.Waiting() != (choice == QuitWait) {
			t.Fatal("等待状态错误")
		}
		if choice == QuitNow {
			if allow, _ := q.Request(3, false); !allow {
				t.Fatal("立即退出不再弹窗")
			}
		}
		if choice == QuitCancel && q.Drained(0) {
			t.Fatal("取消后不能自动退出")
		}
	}
}

func TestQuit_WaitNewRequestsReconfirmAndCancel(t *testing.T) {
	q := &QuitState{}
	q.Request(1, false)
	q.Choose(QuitWait, 1)
	if q.Drained(2) || !q.Drained(0) {
		t.Fatal("等待必须跟随最新请求数")
	}
	allow, prompt := q.Request(2, false)
	if allow || !prompt {
		t.Fatal("等待时再次退出应可立即退出或取消")
	}
	if q.Drained(0) {
		t.Fatal("正在重新确认时不应自动退出")
	}
	q.Choose(QuitCancel, 0)
	if q.Drained(0) {
		t.Fatal("取消后应清除等待")
	}
	q.Request(1, false)
	if !q.Choose(QuitWait, 0) {
		t.Fatal("弹窗期间请求已结束，应直接触发退出")
	}
}

func TestQuit_PowerOffBypassesPendingDialog(t *testing.T) {
	q := &QuitState{}
	q.Request(2, false)
	if allow, prompt := q.Request(2, true); !allow || prompt {
		t.Fatal("关机应跳过正在确认的退出")
	}
}
