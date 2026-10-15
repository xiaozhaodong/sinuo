package main

import (
	"fmt"
	"runtime"
	"time"

	"cc-forwarder/internal/tray"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
)

func (t *appTray) setupWindowEvents() {
	t.window.RegisterKeyBinding("CmdOrCtrl+W", func(application.Window) { t.window.Close() })
	t.window.RegisterKeyBinding("CmdOrCtrl+O", func(application.Window) { t.showWindow("") })
	t.window.RegisterHook(events.Common.WindowClosing, func(event *application.WindowEvent) {
		if t.closed.Load() {
			return
		}
		event.Cancel()
		if shouldLeaveTrayFullscreen(runtime.GOOS, t.window.IsFullscreen()) {
			t.leaveFullscreenThenClose()
			return
		}
		go t.closeWindow()
	})
	// 隐藏到托盘或最小化后才需要系统通知，第一次发生时申请权限
	t.window.OnWindowEvent(events.Common.WindowMinimise, func(*application.WindowEvent) { t.notify.authorize() })
	t.app.wailsApp.Event.RegisterApplicationEventHook(events.Mac.ApplicationShouldHandleReopen, func(event *application.ApplicationEvent) {
		event.Cancel()
		t.showWindow("")
	})
	ready := events.Common.WindowRuntimeReady
	if runtime.GOOS == "windows" {
		ready = events.Windows.WebViewNavigationCompleted
	}
	t.window.OnWindowEvent(ready, func(*application.WindowEvent) {
		if t.windowReady.CompareAndSwap(false, true) && runtime.GOOS == "windows" {
			t.showWindow("")
		}
	})
	if runtime.GOOS == "darwin" {
		t.window.OnWindowEvent(events.Mac.WindowDidExitFullScreen, func(*application.WindowEvent) {
			if t.fullscreenClosing.CompareAndSwap(true, false) {
				go t.closeWindow()
			}
		})
	}
}

func shouldLeaveTrayFullscreen(goos string, fullscreen bool) bool {
	return goos == "darwin" && fullscreen
}

// shouldQuit 不在原生退出回调中等待对话框，防止阻塞主线程。
func (a *App) shouldQuit() bool {
	if a.tray == nil {
		return true
	}
	t := a.tray
	active, powerOff := t.activeCount(), traySystemEnding()
	if t.autoQuit.Swap(false) && !powerOff && t.quit.WaitingWithoutPrompt() {
		// “等完成后退出”的内部重试不能再次弹确认；请求归零才放行。
		return active == 0
	}
	allow, prompt := t.quit.Request(active, powerOff)
	if prompt {
		go t.confirmQuit(active)
	}
	return allow
}

func (t *appTray) confirmQuit(active int) {
	t.activateForDialog()
	dialog := t.app.wailsApp.Dialog.Question().SetTitle("退出 Sinuo")
	dialog.SetMessage(fmt.Sprintf("仍有 %d 个请求进行中，请选择退出方式。", active))
	choose := func(choice tray.QuitChoice) func() {
		return func() {
			quit := t.quit.Choose(choice, t.activeCount())
			t.signal(t.immediate)
			if quit {
				if choice == tray.QuitWait {
					t.autoQuit.Store(true)
				}
				go t.app.wailsApp.Quit()
			}
		}
	}
	dialog.AddButton("等完成后退出").OnClick(choose(tray.QuitWait))
	dialog.AddButton("立即退出").OnClick(choose(tray.QuitNow))
	dialog.AddButton("取消").SetAsDefault().SetAsCancel().OnClick(choose(tray.QuitCancel))
	dialog.Show()
}

func (t *appTray) closeWindow() {
	if !t.windowMu.TryLock() {
		return
	}
	defer t.windowMu.Unlock()
	// 迁移期间设置库尚不可读，按默认驻留行为隐藏；不抢启动锁，也不提前写提示标记。
	if t.app.GetMigrationStatus().State != "ready" {
		t.hideWindow()
		return
	}
	closeToTray, err := t.app.readTraySetting("close_to_tray", true)
	if err != nil {
		t.showError(err)
		return
	}
	if !closeToTray {
		t.app.wailsApp.Quit()
		return
	}
	hintShown, err := t.app.readTraySetting("tray_hint_shown", false)
	if err != nil {
		t.showError(err)
		return
	}
	if !hintShown {
		t.activateForDialog()
		dialog := t.app.wailsApp.Dialog.Info().SetTitle("Sinuo 仍在菜单栏运行")
		dialog.SetMessage("关闭窗口后代理会继续运行。点击系统托盘图标可重新打开窗口，或选择退出。")
		dialog.AddButton("知道了").SetAsDefault().OnClick(func() {
			go func() {
				if err := t.app.UpdateSetting(UpdateSettingInput{Category: "desktop", Key: "tray_hint_shown", Value: "true"}); err != nil {
					t.showError(err)
				}
			}()
		})
		dialog.Show()
	}
	t.hideWindow()
}

func (t *appTray) hideWindow() {
	application.InvokeSync(func() {
		if t.closed.Load() {
			return
		}
		t.window.Hide()
		setTrayWindowVisible(t.window, false)
	})
	t.notify.authorize()
}

func (t *appTray) leaveFullscreenThenClose() {
	if !t.fullscreenClosing.CompareAndSwap(false, true) {
		return
	}
	application.InvokeSync(func() { t.window.UnFullscreen() })
	time.AfterFunc(3*time.Second, func() {
		if t.fullscreenClosing.CompareAndSwap(true, false) {
			go t.closeWindow()
		}
	})
}

func (t *appTray) activateForDialog() {
	application.InvokeSync(trayActivateForDialog)
}

func (t *appTray) showWindow(target string) {
	if t.closed.Load() {
		return
	}
	t.fullscreenClosing.Store(false)
	if target != "" {
		t.navigationMu.Lock()
		t.pendingNavigation = target
		t.navigationMu.Unlock()
	}
	// Windows 第一次导航完成前不调用 Show，避免 WebView2 控制器仍未创建。
	if runtime.GOOS == "windows" && !t.windowReady.Load() {
		return
	}
	application.InvokeSync(func() {
		if t.closed.Load() {
			return
		}
		setTrayWindowVisible(t.window, true)
		t.window.Show()
		if t.window.IsMinimised() {
			t.window.UnMinimise()
		}
		t.window.Focus()
	})
	t.deliverNavigation()
}

func (t *appTray) navigationBecameReady() {
	t.navigationMu.Lock()
	t.navigationReady = true
	t.navigationMu.Unlock()
	t.deliverNavigation()
}

func (t *appTray) deliverNavigation() {
	t.navigationMu.Lock()
	target := t.pendingNavigation
	if !t.navigationReady || target == "" {
		t.navigationMu.Unlock()
		return
	}
	t.pendingNavigation = ""
	t.navigationMu.Unlock()
	t.app.emit("desktop:navigate", target)
}
