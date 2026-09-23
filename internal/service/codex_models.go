package service

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

const (
	CategoryCodexModels        = "codex_models"
	CodexModelsCatalogKey      = "catalog"
	CodexModelsModeLocal       = "local"
	CodexModelsModePassthrough = "passthrough"
)

// CodexModelEntry 表示本地 /v1/models 返回目录中的一个模型。
type CodexModelEntry struct {
	ID          string `json:"id"`
	Object      string `json:"object,omitempty"`
	OwnedBy     string `json:"owned_by,omitempty"`
	Source      string `json:"source,omitempty"`
	Enabled     bool   `json:"enabled"`
	Deprecated  bool   `json:"deprecated,omitempty"`
	DisplayName string `json:"display_name,omitempty"`
	Description string `json:"description,omitempty"`
	// 能力对象不重复保存 slug/display_name/description，由上述字段统一生成。
	CapabilitiesJSON string `json:"capabilities_json,omitempty"`
}

// CodexModelCatalog 是可持久化的本地 Codex 模型目录。
type CodexModelCatalog struct {
	Enabled bool              `json:"enabled"`
	Mode    string            `json:"mode"`
	Models  []CodexModelEntry `json:"models"`
}

// CodexModelsListResponse 是 OpenAI 兼容的 /v1/models 响应结构。
type CodexModelsListResponse struct {
	Object string                  `json:"object"`
	Data   []CodexModelsListObject `json:"data"`
	Models []json.RawMessage       `json:"models"`
}

type CodexModelsListObject struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	OwnedBy string `json:"owned_by"`
}

func DefaultCodexModelCatalog() CodexModelCatalog {
	return NormalizeCodexModelCatalog(CodexModelCatalog{
		Enabled: true,
		Mode:    CodexModelsModeLocal,
		Models: []CodexModelEntry{
			{ID: "gpt-5.5", Source: "official", Enabled: true, DisplayName: "GPT-5.5", Description: "内置预设，发布或长期使用前建议用当前 Codex 上游校准"},
			{ID: "gpt-5.4", Source: "official", Enabled: true, DisplayName: "GPT-5.4", Description: "内置预设，发布或长期使用前建议用当前 Codex 上游校准"},
			{ID: "gpt-5.4-mini", Source: "official", Enabled: true, DisplayName: "GPT-5.4 Mini", Description: "内置预设，发布或长期使用前建议用当前 Codex 上游校准"},
			{ID: "gpt-5.4-nano", Source: "official", Enabled: false, DisplayName: "GPT-5.4 Nano", Description: "内置预设，默认关闭，可按当前账号可用性启用"},
			{ID: "gpt-5.3-codex", Source: "official", Enabled: true, DisplayName: "GPT-5.3 Codex", Description: "内置预设，发布或长期使用前建议用当前 Codex 上游校准"},
			{ID: "gpt-5.2", Source: "official", Enabled: false, DisplayName: "GPT-5.2", Description: "内置预设，默认关闭，可按当前账号可用性启用"},
			{ID: "gpt-5-codex", Source: "official", Enabled: false, Deprecated: true, DisplayName: "GPT-5 Codex", Description: "历史预设，默认关闭，可按当前账号可用性恢复"},
			{ID: "gpt-5.2-codex", Source: "official", Enabled: false, Deprecated: true, DisplayName: "GPT-5.2 Codex", Description: "历史预设，默认关闭，可按当前账号可用性恢复"},
			{ID: "gpt-5.1-codex", Source: "official", Enabled: false, Deprecated: true, DisplayName: "GPT-5.1 Codex", Description: "历史预设，默认关闭，可按当前账号可用性恢复"},
			{ID: "gpt-5.1-codex-max", Source: "official", Enabled: false, Deprecated: true, DisplayName: "GPT-5.1 Codex Max", Description: "历史预设，默认关闭，可按当前账号可用性恢复"},
			{ID: "gpt-5.1-codex-mini", Source: "official", Enabled: false, Deprecated: true, DisplayName: "GPT-5.1 Codex Mini", Description: "历史预设，默认关闭，可按当前账号可用性恢复"},
			{ID: "codex-mini-latest", Source: "official", Enabled: false, Deprecated: true, DisplayName: "Codex Mini Latest", Description: "历史预设，默认关闭，可按当前账号可用性恢复"},
		},
	})
}

func NormalizeCodexModelCatalog(catalog CodexModelCatalog) CodexModelCatalog {
	mode := strings.TrimSpace(strings.ToLower(catalog.Mode))
	if mode == "" {
		mode = CodexModelsModeLocal
	}
	if mode != CodexModelsModeLocal && mode != CodexModelsModePassthrough {
		mode = CodexModelsModeLocal
	}
	catalog.Mode = mode

	normalized := make([]CodexModelEntry, 0, len(catalog.Models))
	indexByID := make(map[string]int)
	for _, model := range catalog.Models {
		model.ID = strings.TrimSpace(model.ID)
		if model.ID == "" {
			continue
		}
		model.Object = strings.TrimSpace(model.Object)
		if model.Object == "" {
			model.Object = "model"
		}
		model.OwnedBy = strings.TrimSpace(model.OwnedBy)
		if model.OwnedBy == "" {
			model.OwnedBy = "openai"
		}
		model.Source = strings.TrimSpace(strings.ToLower(model.Source))
		if model.Source == "" {
			model.Source = "custom"
		}

		if existingIndex, exists := indexByID[model.ID]; exists {
			existing := normalized[existingIndex]
			if existing.Source == "custom" && model.Source != "custom" {
				continue
			}
			normalized[existingIndex] = model
			continue
		}

		indexByID[model.ID] = len(normalized)
		normalized = append(normalized, model)
	}

	sort.SliceStable(normalized, func(i, j int) bool {
		if normalized[i].Enabled != normalized[j].Enabled {
			return normalized[i].Enabled
		}
		if normalized[i].Deprecated != normalized[j].Deprecated {
			return !normalized[i].Deprecated
		}
		if normalized[i].Source != normalized[j].Source {
			return normalized[i].Source == "official"
		}
		return normalized[i].ID < normalized[j].ID
	})

	catalog.Models = normalized
	return catalog
}

func (c CodexModelCatalog) EffectiveModels() []CodexModelEntry {
	models := make([]CodexModelEntry, 0, len(c.Models))
	for _, model := range c.Models {
		if model.Enabled {
			models = append(models, model)
		}
	}
	return models
}

// BuildListResponse 生成 /v1/models 响应。能力无效的模型只从 models 中跳过并返回原因：
// Codex 解析远端目录时单个模型失败即整份丢弃，data 普通列表则不受影响。
func (c CodexModelCatalog) BuildListResponse() (CodexModelsListResponse, []error) {
	models := c.EffectiveModels()
	data := make([]CodexModelsListObject, 0, len(models))
	capabilities := make([]json.RawMessage, 0, len(models))
	var skipped []error
	for _, model := range models {
		if strings.TrimSpace(model.CapabilitiesJSON) != "" {
			if metadata, err := model.BuildCodexModelMetadata(); err != nil {
				skipped = append(skipped, fmt.Errorf("模型 %q：%w", model.ID, err))
			} else {
				capabilities = append(capabilities, metadata)
			}
		}
		object := strings.TrimSpace(model.Object)
		if object == "" {
			object = "model"
		}
		ownedBy := strings.TrimSpace(model.OwnedBy)
		if ownedBy == "" {
			ownedBy = "openai"
		}
		data = append(data, CodexModelsListObject{
			ID:      model.ID,
			Object:  object,
			OwnedBy: ownedBy,
		})
	}
	return CodexModelsListResponse{Object: "list", Data: data, Models: capabilities}, skipped
}

func (s *SettingsService) GetCodexModelCatalog(ctx context.Context) (CodexModelCatalog, error) {
	if s == nil || s.store == nil {
		return DefaultCodexModelCatalog(), nil
	}

	raw, err := s.GetValue(ctx, CategoryCodexModels, CodexModelsCatalogKey)
	if err != nil {
		return CodexModelCatalog{}, err
	}
	if strings.TrimSpace(raw) == "" {
		return DefaultCodexModelCatalog(), nil
	}

	// 读取不做严格校验：旧版本数据或规则收紧不应让整个目录不可用。
	// 无效能力在输出时逐条跳过并通过 CapabilitiesError 标记，保存时再严格校验。
	var catalog CodexModelCatalog
	if err := json.Unmarshal([]byte(raw), &catalog); err != nil {
		return CodexModelCatalog{}, fmt.Errorf("解析 Codex 模型目录失败: %w", err)
	}
	return NormalizeCodexModelCatalog(catalog), nil
}

func (s *SettingsService) SaveCodexModelCatalog(ctx context.Context, catalog CodexModelCatalog) (CodexModelCatalog, error) {
	if s == nil || s.store == nil {
		return CodexModelCatalog{}, fmt.Errorf("设置服务未启用")
	}

	if err := ValidateCodexModelCatalog(catalog); err != nil {
		return CodexModelCatalog{}, err
	}
	normalized := NormalizeCodexModelCatalog(catalog)
	payload, err := json.Marshal(normalized)
	if err != nil {
		return CodexModelCatalog{}, fmt.Errorf("序列化 Codex 模型目录失败: %w", err)
	}
	if err := s.Set(ctx, CategoryCodexModels, CodexModelsCatalogKey, string(payload)); err != nil {
		return CodexModelCatalog{}, err
	}
	return normalized, nil
}

func (s *SettingsService) MergeDefaultCodexModelCatalog(ctx context.Context) (CodexModelCatalog, error) {
	current, err := s.GetCodexModelCatalog(ctx)
	if err != nil {
		return CodexModelCatalog{}, err
	}
	defaults := DefaultCodexModelCatalog()

	mergedByID := make(map[string]CodexModelEntry)
	for _, model := range defaults.Models {
		mergedByID[model.ID] = model
	}
	for _, model := range current.Models {
		if official, ok := mergedByID[model.ID]; ok && model.Source != "custom" {
			model.Source = official.Source
			model.Deprecated = official.Deprecated
			if strings.TrimSpace(model.DisplayName) == "" {
				model.DisplayName = official.DisplayName
			}
			if strings.TrimSpace(model.Description) == "" {
				model.Description = official.Description
			}
		}
		mergedByID[model.ID] = model
	}

	merged := CodexModelCatalog{
		Enabled: current.Enabled,
		Mode:    current.Mode,
		Models:  make([]CodexModelEntry, 0, len(mergedByID)),
	}
	for _, model := range mergedByID {
		merged.Models = append(merged.Models, model)
	}
	return s.SaveCodexModelCatalog(ctx, merged)
}

func (s *SettingsService) ResetCodexModelCatalog(ctx context.Context) (CodexModelCatalog, error) {
	return s.SaveCodexModelCatalog(ctx, DefaultCodexModelCatalog())
}
