package main

import (
	"sinuo/internal/monitor"
	"sinuo/internal/tray"
)

const (
	trayDrainOwner   = "tray-quit"
	updateDrainOwner = "update-restart"
)

func (a *App) requestActivity() *monitor.RequestActivity {
	if a == nil || a.monitoringMiddleware == nil {
		return nil
	}
	return a.monitoringMiddleware.GetMetrics().RequestActivity()
}

func (a *App) activeRequestCount() int64 {
	if activity := a.requestActivity(); activity != nil {
		return int64(activity.Count())
	}
	return 0
}

func (a *App) setRequestDrain(owner string, draining bool) {
	if activity := a.requestActivity(); activity != nil {
		if draining {
			activity.BeginDrain(owner)
		} else {
			activity.CancelDrain(owner)
		}
	}
}

func (t *appTray) requestActivity() *monitor.RequestActivity {
	if metrics := t.metrics.Load(); metrics != nil {
		return metrics.RequestActivity()
	}
	return nil
}

// requestQuit 先封住接收再判断空闲；弹确认框期间继续接收，选择等待后才持续排空。
func (t *appTray) requestQuit(powerOff bool) (allow, prompt bool, active int) {
	activity := t.requestActivity()
	if activity != nil {
		active = activity.BeginDrain(trayDrainOwner)
	}
	if t.autoQuit.Swap(false) && !powerOff && t.quit.WaitingWithoutPrompt() {
		return active == 0, false, active
	}
	allow, prompt = t.quit.Request(active, powerOff)
	if !allow && !t.quit.Waiting() && activity != nil {
		activity.CancelDrain(trayDrainOwner)
	}
	return allow, prompt, active
}

func (t *appTray) chooseQuit(choice tray.QuitChoice) bool {
	if choice == tray.QuitCancel {
		// 先撤销等待状态，再恢复接收，避免变化通知仍按等待退出处理。
		quit := t.quit.Choose(choice, 0)
		if activity := t.requestActivity(); activity != nil {
			activity.CancelDrain(trayDrainOwner)
		}
		return quit
	}
	active := 0
	if activity := t.requestActivity(); activity != nil {
		active = activity.BeginDrain(trayDrainOwner)
	}
	return t.finishQuitChoice(choice, active)
}

// finishQuitChoice 在 BeginDrain 采样后应用选择，单独保留这一阶段以验证通知先到的时序。
func (t *appTray) finishQuitChoice(choice tray.QuitChoice, active int) bool {
	if t.quit.Choose(choice, active) {
		return true
	}
	// 先设置 waiting，再读最新计数；即使归零通知已被消费，也由选择动作主动复查。
	return choice == tray.QuitWait && t.quit.Drained(t.activeCount())
}
