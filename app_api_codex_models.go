package main

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"sinuo/internal/service"
)

type CodexModelEntryInfo struct {
	ID               string `json:"id"`
	Object           string `json:"object"`
	OwnedBy          string `json:"owned_by"`
	Source           string `json:"source"`
	Enabled          bool   `json:"enabled"`
	Deprecated       bool   `json:"deprecated"`
	DisplayName      string `json:"display_name"`
	Description      string `json:"description"`
	CapabilitiesJSON string `json:"capabilities_json"`
	// CapabilitiesError 只读：已保存能力无效的原因，保存时忽略。
	CapabilitiesError string `json:"capabilities_error"`
}

type CodexModelCatalogInfo struct {
	Enabled        bool                  `json:"enabled"`
	Mode           string                `json:"mode"`
	Models         []CodexModelEntryInfo `json:"models"`
	EffectiveCount int                   `json:"effective_count"`
}

type SaveCodexModelCatalogInput struct {
	Enabled bool                  `json:"enabled"`
	Mode    string                `json:"mode"`
	Models  []CodexModelEntryInfo `json:"models"`
}

func (a *App) GetCodexModelCatalog() (CodexModelCatalogInfo, error) {
	a.mu.RLock()
	defer a.mu.RUnlock()

	if a.settingsService == nil {
		return CodexModelCatalogInfo{}, fmt.Errorf("设置服务未启用")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	catalog, err := a.settingsService.GetCodexModelCatalog(ctx)
	if err != nil {
		return CodexModelCatalogInfo{}, err
	}
	return codexModelCatalogToInfo(catalog), nil
}

func (a *App) SaveCodexModelCatalog(input SaveCodexModelCatalogInput) (CodexModelCatalogInfo, error) {
	a.mu.RLock()
	defer a.mu.RUnlock()

	if a.settingsService == nil {
		return CodexModelCatalogInfo{}, fmt.Errorf("设置服务未启用")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	catalog, err := a.settingsService.SaveCodexModelCatalog(ctx, codexModelCatalogFromInput(input))
	if err != nil {
		return CodexModelCatalogInfo{}, err
	}
	if a.logger != nil {
		a.logger.Info("✅ Codex 模型目录已保存", "enabled", catalog.Enabled, "mode", catalog.Mode, "models", len(catalog.Models))
	}
	return codexModelCatalogToInfo(catalog), nil
}

func (a *App) MergeDefaultCodexModelCatalog() (CodexModelCatalogInfo, error) {
	a.mu.RLock()
	defer a.mu.RUnlock()

	if a.settingsService == nil {
		return CodexModelCatalogInfo{}, fmt.Errorf("设置服务未启用")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	catalog, err := a.settingsService.MergeDefaultCodexModelCatalog(ctx)
	if err != nil {
		return CodexModelCatalogInfo{}, err
	}
	if a.logger != nil {
		a.logger.Info("✅ Codex 官方模型预设已合并", "models", len(catalog.Models))
	}
	return codexModelCatalogToInfo(catalog), nil
}

func (a *App) ResetCodexModelCatalog() (CodexModelCatalogInfo, error) {
	a.mu.RLock()
	defer a.mu.RUnlock()

	if a.settingsService == nil {
		return CodexModelCatalogInfo{}, fmt.Errorf("设置服务未启用")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	catalog, err := a.settingsService.ResetCodexModelCatalog(ctx)
	if err != nil {
		return CodexModelCatalogInfo{}, err
	}
	if a.logger != nil {
		a.logger.Info("✅ Codex 模型目录已恢复官方预设", "models", len(catalog.Models))
	}
	return codexModelCatalogToInfo(catalog), nil
}

type codexModelListProvider struct {
	app *App
}

func (p *codexModelListProvider) GetCodexModelListResponse(ctx context.Context) ([]byte, bool, error) {
	if p == nil || p.app == nil || p.app.settingsService == nil {
		return nil, false, nil
	}

	catalog, err := p.app.settingsService.GetCodexModelCatalog(ctx)
	if err != nil {
		return nil, true, err
	}
	if !catalog.Enabled || catalog.Mode == service.CodexModelsModePassthrough {
		return nil, false, nil
	}

	response, skipped := catalog.BuildListResponse()
	if p.app.logger != nil {
		for _, err := range skipped {
			p.app.logger.Warn("⚠️ Codex 模型能力无效，已从 models 中跳过", "error", err)
		}
	}
	payload, err := json.Marshal(response)
	if err != nil {
		return nil, true, fmt.Errorf("生成 Codex 模型列表失败: %w", err)
	}
	return payload, true, nil
}

func codexModelCatalogFromInput(input SaveCodexModelCatalogInput) service.CodexModelCatalog {
	models := make([]service.CodexModelEntry, 0, len(input.Models))
	for _, model := range input.Models {
		models = append(models, service.CodexModelEntry{
			ID:               model.ID,
			Object:           model.Object,
			OwnedBy:          model.OwnedBy,
			Source:           model.Source,
			Enabled:          model.Enabled,
			Deprecated:       model.Deprecated,
			DisplayName:      model.DisplayName,
			Description:      model.Description,
			CapabilitiesJSON: model.CapabilitiesJSON,
		})
	}
	return service.CodexModelCatalog{
		Enabled: input.Enabled,
		Mode:    input.Mode,
		Models:  models,
	}
}

func codexModelCatalogToInfo(catalog service.CodexModelCatalog) CodexModelCatalogInfo {
	normalized := service.NormalizeCodexModelCatalog(catalog)
	models := make([]CodexModelEntryInfo, 0, len(normalized.Models))
	for _, model := range normalized.Models {
		models = append(models, CodexModelEntryInfo{
			ID:                model.ID,
			Object:            model.Object,
			OwnedBy:           model.OwnedBy,
			Source:            model.Source,
			Enabled:           model.Enabled,
			Deprecated:        model.Deprecated,
			DisplayName:       model.DisplayName,
			Description:       model.Description,
			CapabilitiesJSON:  model.CapabilitiesJSON,
			CapabilitiesError: model.CapabilitiesError(),
		})
	}
	return CodexModelCatalogInfo{
		Enabled:        normalized.Enabled,
		Mode:           normalized.Mode,
		Models:         models,
		EffectiveCount: len(normalized.EffectiveModels()),
	}
}
