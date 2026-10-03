package main

import (
	"reflect"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"cc-forwarder/internal/monitor"
	"cc-forwarder/internal/tray"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
)

type appTray struct {
	app                                              *App
	window                                           *application.WebviewWindow
	native                                           *application.SystemTray
	menu                                             *application.Menu
	model                                            []tray.Item // 只由刷新循环读写
	refresh, immediate, open, done                   chan struct{}
	closed, windowReady, autoQuit, fullscreenClosing atomic.Bool
	metrics                                          atomic.Pointer[monitor.Metrics]
	quit                                             tray.QuitState
	actionMu                                         sync.Mutex
	windowMu                                         sync.Mutex
	navigationMu                                     sync.Mutex
	navigationReady                                  bool
	pendingNavigation                                string
	stats                                            UsageSummary
	statsAt                                          time.Time
	statsErr                                         error
	icon                                             trayIcon
	tooltip                                          string // 只由刷新循环读写
}

type trayIconSetter struct{ native *application.SystemTray }

func (s trayIconSetter) SetFrame(png []byte, template bool) {
	if template {
		s.native.SetTemplateIcon(png)
	} else {
		s.native.SetIcon(png)
	}
}

func (a *App) setupTray(window *application.WebviewWindow) {
	native := a.wailsApp.SystemTray.New()
	t := &appTray{app: a, window: window, native: native, menu: application.NewMenu(),
		refresh: make(chan struct{}, 1), immediate: make(chan struct{}, 1), open: make(chan struct{}, 1), done: make(chan struct{}),
		icon: newTrayIcon(trayIconSetter{native})}
	a.tray = t
	t.applyIcon(tray.IconStopped, -1)
	t.native.SetTooltip("AI Switchboard · 正在启动")
	t.appendMenu(t.menu, tray.Build(tray.Snapshot{StartupState: "正在启动"}))
	t.native.SetMenu(t.menu)
	if runtime.GOOS != "linux" {
		t.native.OnClick(func() {
			if runtime.GOOS == "windows" {
				t.showWindow("")
				return
			}
			t.signal(t.open)
		})
		t.native.OnRightClick(func() { t.signal(t.open) })
	} else {
		// beta.27 的 Linux OpenMenu 是空实现；保留默认 SecondaryActivate，
		// 让桌面环境的右键 D-Bus 菜单仍能弹出，左键由桌面环境自行处理。
		t.native.OnClick(func() { t.signal(t.open) })
	}
	t.setupWindowEvents()
	a.wailsApp.Event.On("desktop:navigation-ready", func(*application.CustomEvent) { t.navigationBecameReady() })
	a.wailsApp.Event.OnApplicationEvent(events.Common.ApplicationStarted, func(*application.ApplicationEvent) {
		application.InvokeSync(installTrayPlatformHooks)
		go t.refreshLoop()
		go t.iconLoop()
		t.signal(t.immediate)
	})
}

func (t *appTray) signal(ch chan struct{}) {
	if t.closed.Load() {
		return
	}
	select {
	case ch <- struct{}{}:
	default:
	}
}

func (a *App) notifyTray(name string) {
	if a.tray == nil {
		return
	}
	if trayRebuildEvent(name) {
		a.tray.signal(a.tray.refresh)
	}
}

func trayRebuildEvent(name string) bool {
	switch name {
	case EventClaudeRoutingUpdate, EventModelRoutingUpdate, EventClaudeCodeEntryUpdate,
		EventClaudeSubscriptionRateLimit, EventEndpointUpdate, EventCodexSubscriptionProxyStatus,
		EventConfigReloaded:
		return true
	default:
		return false
	}
}

// refreshLoop 串行读快照和更新菜单；事件在 300ms 内合并，点击不等防抖。
func (t *appTray) refreshLoop() {
	var timer *time.Timer
	var changes <-chan time.Time
	defer func() {
		if timer != nil {
			timer.Stop()
		}
	}()
	for {
		select {
		case <-t.done:
			return
		case <-t.open:
			t.rebuild(true, true)
		case <-t.immediate:
			t.rebuild(false, true)
		case <-t.refresh:
			if changes == nil {
				timer = time.NewTimer(300 * time.Millisecond)
				changes = timer.C
			}
		case <-changes:
			changes = nil
			t.rebuild(false, false)
		}
	}
}

func (t *appTray) rebuild(open, force bool) {
	snapshot := t.snapshot()
	model := tray.Build(snapshot)
	tooltip := trayTooltip(model[0].Label, t.setIconStatus(snapshot))
	changed := force || !reflect.DeepEqual(t.model, model)
	tooltipChanged := tooltip != t.tooltip
	if t.closed.Load() || (!changed && !open && !tooltipChanged) {
		return
	}
	application.InvokeSync(func() {
		if t.closed.Load() {
			return
		}
		if changed {
			if sameTrayMenuShape(t.model, model) {
				updateTrayMenu(t.menu, model)
			} else {
				// 拓扑变化才替换菜单项；统计、勾选与标签变化保留正在展开的子菜单。
				t.menu.Destroy()
				t.appendMenu(t.menu, model)
				t.native.SetMenu(t.menu)
			}
		}
		if tooltipChanged {
			t.native.SetTooltip(tooltip)
		}
		if open && runtime.GOOS != "linux" {
			t.native.OpenMenu()
		}
	})
	t.model, t.tooltip = model, tooltip
}

func sameTrayMenuShape(previous, next []tray.Item) bool {
	if len(previous) != len(next) {
		return false
	}
	for i, item := range next {
		old := previous[i]
		if old.Separator != item.Separator || old.Checkable != item.Checkable || old.Action != item.Action ||
			!sameTrayMenuShape(old.Children, item.Children) {
			return false
		}
	}
	return true
}

func updateTrayMenu(menu *application.Menu, items []tray.Item) {
	for i, item := range items {
		if item.Separator {
			continue
		}
		native := menu.ItemAt(i)
		native.SetLabel(item.Label).SetEnabled(!item.Disabled)
		if item.Checkable {
			native.SetChecked(item.Checked)
		}
		if len(item.Children) > 0 {
			updateTrayMenu(native.GetSubmenu(), item.Children)
		}
	}
}

func (t *appTray) appendMenu(menu *application.Menu, items []tray.Item) {
	for _, item := range items {
		if item.Separator {
			menu.AddSeparator()
			continue
		}
		if len(item.Children) > 0 {
			t.appendMenu(menu.AddSubmenu(item.Label), item.Children)
			continue
		}
		var native *application.MenuItem
		if item.Checkable {
			native = menu.AddCheckbox(item.Label, item.Checked)
		} else {
			native = menu.Add(item.Label)
		}
		native.SetEnabled(!item.Disabled)
		if item.Shortcut != "" {
			native.SetAccelerator(item.Shortcut)
		}
		if item.Action.Kind != "" {
			action := item.Action
			native.OnClick(func(*application.Context) { go t.runAction(action) })
		}
	}
}

func (t *appTray) runAction(action tray.Action) {
	if t.closed.Load() {
		return
	}
	switch action.Kind {
	case "navigate":
		t.showWindow(action.Target)
		return
	case "quit":
		t.app.wailsApp.Quit()
		return
	}
	t.actionMu.Lock()
	defer t.actionMu.Unlock()
	if err := t.app.executeTrayAction(action); err != nil {
		t.showError(err)
	}
	// 包括失败后的读回，清除原生 checkbox 在回调前自行切换的显示状态。
	t.signal(t.immediate)
	t.app.emit("desktop:settings-update", nil)
}

func (t *appTray) showError(err error) {
	if t.closed.Load() {
		return
	}
	t.activateForDialog()
	t.app.wailsApp.Dialog.Error().SetTitle("AI Switchboard").SetMessage(err.Error()).Show()
}

func (a *App) attachTrayMetrics(metrics *monitor.Metrics) {
	if a.tray == nil {
		return
	}
	t := a.tray
	t.metrics.Store(metrics)
	changes, cancel := metrics.SubscribeActiveConnections()
	go func() {
		defer cancel()
		for {
			select {
			case <-t.done:
				return
			case _, ok := <-changes:
				if !ok || t.metrics.Load() != metrics {
					return
				}
				t.handleActiveConnectionChange()
			}
		}
	}()
}

func (t *appTray) handleActiveConnectionChange() {
	t.signal(t.icon.wake)
	active := t.activeCount()
	if t.quit.Waiting() {
		t.signal(t.refresh)
	}
	if t.quit.Drained(active) {
		t.autoQuit.Store(true)
		t.app.wailsApp.Quit()
	}
}

func (t *appTray) activeCount() int {
	if metrics := t.metrics.Load(); metrics != nil {
		return metrics.ActiveConnectionCount()
	}
	return 0
}

func (a *App) stopTray() {
	if a.tray != nil && a.tray.closed.CompareAndSwap(false, true) {
		close(a.tray.done)
	}
}
