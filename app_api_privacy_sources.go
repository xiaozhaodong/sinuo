package main

import (
	"sinuo/internal/service"
	"sinuo/internal/store"
)

// ListPrivacyConfigSources 仅返回登记路径、同步状态和数量，不返回文件内容。
func (a *App) ListPrivacyConfigSources() (service.PrivacyConfigSourceState, error) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	svc, err := a.privacyServiceOrError()
	if err != nil {
		return service.PrivacyConfigSourceState{}, err
	}
	ctx, cancel := privacyAPIContext()
	defer cancel()
	return svc.ListConfigSources(ctx)
}

// SavePrivacyConfigSource 登记用户明确指定的位置；保存后由后台扫描并记录同步结果。
func (a *App) SavePrivacyConfigSource(input store.PrivacyConfigSourceRecord) (*store.PrivacyConfigSourceRecord, error) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	svc, err := a.privacyServiceOrError()
	if err != nil {
		return nil, err
	}
	ctx, cancel := privacyAPIContext()
	defer cancel()
	return svc.SaveConfigSource(ctx, input)
}

// DeletePrivacyConfigSource 移除登记，已导入敏感值保留。
func (a *App) DeletePrivacyConfigSource(id int64) error {
	a.mu.RLock()
	defer a.mu.RUnlock()
	svc, err := a.privacyServiceOrError()
	if err != nil {
		return err
	}
	ctx, cancel := privacyAPIContext()
	defer cancel()
	return svc.DeleteConfigSource(ctx, id)
}

// SyncPrivacyConfigSources 立即检查一个来源；id=0 表示全部启用来源。
func (a *App) SyncPrivacyConfigSources(id int64) error {
	a.mu.RLock()
	defer a.mu.RUnlock()
	svc, err := a.privacyServiceOrError()
	if err != nil {
		return err
	}
	ctx, cancel := privacyAPIContext()
	defer cancel()
	return svc.SyncConfigSources(ctx, id)
}

// PreviewPrivacyConfigSourceImport 预览已登记来源，只返回掩码候选，无需前端重新选文件。
func (a *App) PreviewPrivacyConfigSourceImport(sourceID int64) (PrivacyConfigImportPreview, error) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	svc, err := a.privacyServiceOrError()
	if err != nil {
		return PrivacyConfigImportPreview{}, err
	}
	ctx, cancel := privacyAPIContext()
	defer cancel()
	preview, err := svc.PreviewConfigSourceImport(ctx, sourceID)
	if err != nil {
		return PrivacyConfigImportPreview{}, err
	}
	return privacyConfigImportPreviewDTO(preview), nil
}
