package main

import (
	"sinuo/internal/privacy/configimport"
)

// PrivacyConfigImportFile 只包含用户主动选择的文件内容，不接受后端文件路径。
type PrivacyConfigImportFile struct {
	Name    string `json:"name"`
	Content string `json:"content"`
}

// 导入接口的桌面 DTO 只返回掩码和来源，原值由 service 临时持有。
type PrivacyConfigImportCandidate struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Category    string   `json:"category"`
	MaskedValue string   `json:"masked_value"`
	ValueLength int      `json:"value_length"`
	Sources     []string `json:"sources"`
	Reason      string   `json:"reason"`
	Status      string   `json:"status"`
	Selectable  bool     `json:"selectable"`
	Selected    bool     `json:"selected"`
}

type PrivacyConfigImportFileResult struct {
	Name    string `json:"name"`
	Format  string `json:"format"`
	Scanned int    `json:"scanned"`
	Found   int    `json:"found"`
	Error   string `json:"error"`
}

type PrivacyConfigImportPreview struct {
	ID         string                          `json:"id"`
	ExpiresAt  string                          `json:"expires_at"`
	Files      []PrivacyConfigImportFileResult `json:"files"`
	Candidates []PrivacyConfigImportCandidate  `json:"candidates"`
}

type PrivacyConfigImportResult struct {
	Added   int `json:"added"`
	Skipped int `json:"skipped"`
}

// PreviewPrivacyConfigImport 本机解析配置并生成候选，不写库，不返回敏感值原文。
func (a *App) PreviewPrivacyConfigImport(files []PrivacyConfigImportFile) (PrivacyConfigImportPreview, error) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	var out PrivacyConfigImportPreview
	svc, err := a.privacyServiceOrError()
	if err != nil {
		return out, err
	}
	ctx, cancel := privacyAPIContext()
	defer cancel()
	input := make([]configimport.File, len(files))
	for i, f := range files {
		input[i] = configimport.File{Name: f.Name, Content: f.Content}
	}
	preview, err := svc.PreviewConfigImport(ctx, input)
	if err != nil {
		return out, err
	}
	out.ID, out.ExpiresAt = preview.ID, preview.ExpiresAt
	out.Files = make([]PrivacyConfigImportFileResult, 0, len(preview.Files))
	for _, f := range preview.Files {
		out.Files = append(out.Files, PrivacyConfigImportFileResult{f.Name, f.Format, f.Scanned, f.Found, f.Error})
	}
	out.Candidates = make([]PrivacyConfigImportCandidate, 0, len(preview.Candidates))
	for _, c := range preview.Candidates {
		out.Candidates = append(out.Candidates, PrivacyConfigImportCandidate{
			ID: c.ID, Name: c.Name, Category: c.Category, MaskedValue: c.MaskedValue,
			ValueLength: c.ValueLength, Sources: c.Sources, Reason: c.Reason,
			Status: c.Status, Selectable: c.Selectable, Selected: c.Selected,
		})
	}
	return out, nil
}

// CommitPrivacyConfigImport 事务追加选中项，并发布一次运行快照。
func (a *App) CommitPrivacyConfigImport(previewID string, candidateIDs []string) (PrivacyConfigImportResult, error) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	svc, err := a.privacyServiceOrError()
	if err != nil {
		return PrivacyConfigImportResult{}, err
	}
	ctx, cancel := privacyAPIContext()
	defer cancel()
	result, err := svc.CommitConfigImport(ctx, previewID, candidateIDs)
	return PrivacyConfigImportResult{Added: result.Added, Skipped: result.Skipped}, err
}

// CancelPrivacyConfigImport 释放本次预览中的临时敏感值。
func (a *App) CancelPrivacyConfigImport(previewID string) error {
	a.mu.RLock()
	defer a.mu.RUnlock()
	svc, err := a.privacyServiceOrError()
	if err != nil {
		return err
	}
	svc.CancelConfigImport(previewID)
	return nil
}
