package main

import (
	"context"
	"encoding/json"
	"fmt"

	"sinuo/internal/service"
)

type claudeModelListProvider struct {
	app *App
}

func (p *claudeModelListProvider) GetClaudeModelListResponse(ctx context.Context) ([]byte, error) {
	if p == nil || p.app == nil || p.app.settingsService == nil {
		return nil, fmt.Errorf("设置服务未启用")
	}

	catalog, err := p.app.settingsService.GetClaudeModelCatalog(ctx)
	if err != nil {
		return nil, err
	}
	payload, err := json.Marshal(catalog.BuildListResponse())
	if err != nil {
		return nil, fmt.Errorf("生成 Claude 模型列表失败: %w", err)
	}
	return payload, nil
}

func normalizeClaudeModelSetting(input *UpdateSettingInput) error {
	if input == nil || input.Category != service.CategoryClaudeModels {
		return nil
	}
	if input.Key != service.ClaudeModelsCatalogKey {
		return fmt.Errorf("不支持的 Claude 模型设置键 %q", input.Key)
	}

	catalog, err := service.DecodeClaudeModelCatalog(input.Value)
	if err != nil {
		return err
	}
	payload, err := json.Marshal(catalog)
	if err != nil {
		return fmt.Errorf("序列化 Claude 模型目录失败: %w", err)
	}
	input.Value = string(payload)
	return nil
}
