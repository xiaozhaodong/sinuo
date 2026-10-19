package main

import (
	"context"
	"errors"
	"os"
	"time"

	"sinuo/internal/store"
)

const (
	appUpdatePendingVersionKey = "pending_version"
	appUpdatePendingFromKey    = "pending_from"
)

// RestartToUpdateResult 在有活跃请求时把决定交还给用户。
type RestartToUpdateResult struct {
	NeedsConfirm   bool  `json:"needs_confirm"`
	ActiveRequests int64 `json:"active_requests"`
}

func (m *appUpdateManager) requestCount() int64 {
	if m.activeRequests == nil {
		return 0
	}
	return m.activeRequests()
}

// RestartToUpdate 空 mode 空闲时直接重启；前端必须先完成用户确认。
func (a *App) RestartToUpdate(mode string) (RestartToUpdateResult, error) {
	m := a.appUpdateRuntime()
	if m == nil {
		return RestartToUpdateResult{}, errors.New("没有已就绪的更新")
	}
	return m.restartToUpdate(mode)
}

func (m *appUpdateManager) restartToUpdate(mode string) (RestartToUpdateResult, error) {
	if mode != "" && mode != "wait" && mode != "now" {
		return RestartToUpdateResult{}, errors.New("无效的更新重启模式")
	}
	m.checkMu.Lock()
	if m.rootCtx.Err() != nil {
		m.checkMu.Unlock()
		return RestartToUpdateResult{}, context.Canceled
	}
	if m.status().State != "ready" || m.restart == nil {
		m.checkMu.Unlock()
		return RestartToUpdateResult{}, errors.New("没有已就绪的更新")
	}
	active := m.requestCount()
	result := RestartToUpdateResult{ActiveRequests: active}
	if mode == "" && active > 0 {
		result.NeedsConfirm = true
		m.checkMu.Unlock()
		return result, nil
	}
	if mode == "wait" {
		ctx, cancel := context.WithCancel(m.rootCtx)
		m.waitCancel = cancel
		m.update(func(s *AppUpdateStatus) { s.State, s.ActiveRequests = "waiting_idle", active })
		m.checkMu.Unlock()
		go m.waitForIdle(ctx)
		return result, nil
	}
	return result, m.beginRestartLocked()
}

func (m *appUpdateManager) waitForIdle(ctx context.Context) {
	ticker := time.NewTicker(m.waitInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.checkMu.Lock()
			if ctx.Err() != nil || m.status().State != "waiting_idle" {
				m.checkMu.Unlock()
				return
			}
			active := m.requestCount()
			if active == 0 {
				// 同一把锁仲裁取消与启动，取消成功后不会再触发重启。
				_ = m.beginRestartLocked()
				return
			}
			if active != m.status().ActiveRequests {
				m.update(func(s *AppUpdateStatus) { s.ActiveRequests = active })
			}
			m.checkMu.Unlock()
		}
	}
}

// 调用时持有 checkMu，返回前释放；调用外部 Restart 时绝不持有锁，避免 Quit 重入关停。
func (m *appUpdateManager) beginRestartLocked() error {
	version := m.status().ReadyVersion
	if err := m.writePending(version, Version); err != nil {
		if m.waitCancel != nil {
			m.waitCancel()
			m.waitCancel = nil
		}
		m.update(func(s *AppUpdateStatus) {
			s.State, s.ActiveRequests, s.ErrorStage, s.Error = "error", 0, "install", err.Error()
		})
		m.checkMu.Unlock()
		return err
	}
	if m.waitCancel != nil {
		m.waitCancel()
		m.waitCancel = nil
	}
	m.app.updateRestarting.Store(true)
	m.update(func(s *AppUpdateStatus) { s.State, s.ActiveRequests, s.Error, s.ErrorStage = "restarting", 0, "", "" })
	m.checkMu.Unlock()
	err := m.restart(m.rootCtx)
	if err == nil {
		return nil
	}
	m.checkMu.Lock()
	defer m.checkMu.Unlock()
	m.app.updateRestarting.Store(false)
	if m.rootCtx.Err() == nil {
		if clearErr := m.writePending("", ""); clearErr != nil {
			err = errors.Join(err, clearErr)
		}
	}
	m.logger.Warn("更新重启失败", "error", err)
	m.update(func(s *AppUpdateStatus) {
		s.State, s.ErrorStage, s.Error = "error", "install", "重启更新失败，请重新下载后重试或前往下载"
	})
	return errors.New("重启更新失败，App 将继续运行")
}

// CancelAppUpdateRestart 取消等待，已经开始的重启不能取消。
func (a *App) CancelAppUpdateRestart() error {
	m := a.appUpdateRuntime()
	if m == nil {
		return errors.New("没有等待中的更新重启")
	}
	m.checkMu.Lock()
	defer m.checkMu.Unlock()
	if m.status().State != "waiting_idle" {
		return errors.New("没有等待中的更新重启")
	}
	m.waitCancel()
	m.waitCancel = nil
	m.update(func(s *AppUpdateStatus) { s.State, s.ActiveRequests = "ready", 0 })
	return nil
}

func (m *appUpdateManager) writePending(version, from string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// 两个键原子提交，避免部分写入误报下一次启动的更新结果。
	err := m.app.settingsStore.BatchSet(ctx, []*store.SettingRecord{
		{Category: appUpdateStateCategory, Key: appUpdatePendingVersionKey, Value: version, ValueType: "string"},
		{Category: appUpdateStateCategory, Key: appUpdatePendingFromKey, Value: from, ValueType: "string"},
	})
	if err != nil {
		m.logger.Warn("更新回执状态保存失败", "error", err)
		return errors.New("保存更新回执失败")
	}
	return nil
}

func (m *appUpdateManager) consumePendingUpdate() {
	version, err := m.readSetting(appUpdatePendingVersionKey)
	if err != nil || version == "" {
		return
	}
	from, err := m.readSetting(appUpdatePendingFromKey)
	if err != nil {
		return
	}
	if m.notify != nil {
		if version == Version {
			m.notify("success", "已更新到 "+version, "Sinuo 已完成更新")
		} else {
			m.notify("warning", "更新到 "+version+" 未完成，当前仍是 "+Version, "可在软件更新中重试或前往下载")
			m.logger.Warn("上次更新未完成", "target", version, "from", from, "current", Version, "helper_log_dir", os.TempDir())
		}
	}
	if err := m.writePending("", ""); err != nil {
		m.setError(err.Error())
	}
}
