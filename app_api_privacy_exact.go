package main

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"sinuo/internal/service"
	"sinuo/internal/store"
)

// PrivacyExactSecretInfo 本地精确敏感值列表响应；不包含 secret_value 明文。
type PrivacyExactSecretInfo struct {
	ID             int64  `json:"id"`
	Enabled        bool   `json:"enabled"`
	Name           string `json:"name"`
	Category       string `json:"category"`
	Placeholder    string `json:"placeholder"`
	SourceType     string `json:"source_type"`
	SourceRef      string `json:"source_ref"`
	Description    string `json:"description"`
	MaskedValue    string `json:"masked_value"`
	ValueLength    int    `json:"value_length"`
	ValueHashShort string `json:"value_hash_short"`
	CreatedAt      string `json:"created_at"`
	UpdatedAt      string `json:"updated_at"`
}

// PrivacyExactSecretInput 创建/更新本地精确敏感值。SecretValue 不写日志、不回传。
type PrivacyExactSecretInput struct {
	Enabled     bool   `json:"enabled"`
	Name        string `json:"name"`
	SecretValue string `json:"secret_value"`
	Category    string `json:"category"`
	Placeholder string `json:"placeholder"`
	SourceType  string `json:"source_type"`
	SourceRef   string `json:"source_ref"`
	Description string `json:"description"`
}

// PrivacySecretImportCandidateInfo 可导入凭据候选；不包含原文。
type PrivacySecretImportCandidateInfo struct {
	SourceType     string `json:"source_type"`
	SourceRef      string `json:"source_ref"`
	Name           string `json:"name"`
	Category       string `json:"category"`
	MaskedValue    string `json:"masked_value"`
	ValueLength    int    `json:"value_length"`
	ValueHashShort string `json:"value_hash_short"`
	AlreadyExists  bool   `json:"already_exists"`
}

// ImportPrivacySecretCandidateInput 导入候选。manual 模式可直接携带 SecretValue。
type ImportPrivacySecretCandidateInput struct {
	SourceType  string `json:"source_type"`
	SourceRef   string `json:"source_ref"`
	Name        string `json:"name"`
	Category    string `json:"category"`
	Placeholder string `json:"placeholder"`
	Description string `json:"description"`
	SecretValue string `json:"secret_value"`
}

// ListPrivacyExactSecrets 列出本地精确敏感值，不返回明文。
func (a *App) ListPrivacyExactSecrets() ([]PrivacyExactSecretInfo, error) {
	a.mu.RLock()
	defer a.mu.RUnlock()

	svc, err := a.privacyServiceOrError()
	if err != nil {
		return nil, err
	}
	ctx, cancel := privacyAPIContext()
	defer cancel()
	records, err := svc.ListExactSecrets(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]PrivacyExactSecretInfo, 0, len(records))
	for _, record := range records {
		out = append(out, buildPrivacyExactSecretInfo(record))
	}
	return out, nil
}

// CreatePrivacyExactSecret 新增本地精确敏感值并热生效。
func (a *App) CreatePrivacyExactSecret(input PrivacyExactSecretInput) (PrivacyExactSecretInfo, error) {
	a.mu.RLock()
	defer a.mu.RUnlock()

	svc, err := a.privacyServiceOrError()
	if err != nil {
		return PrivacyExactSecretInfo{}, err
	}
	ctx, cancel := privacyAPIContext()
	defer cancel()
	created, err := svc.CreateExactSecret(ctx, privacyExactSecretRecordFromInput(0, input))
	if err != nil {
		return PrivacyExactSecretInfo{}, privacyExactSecretAPIError(err)
	}
	return buildPrivacyExactSecretInfo(created), nil
}

// UpdatePrivacyExactSecret 更新本地精确敏感值；secret_value 为空时仅更新元数据。
func (a *App) UpdatePrivacyExactSecret(id int64, input PrivacyExactSecretInput) (PrivacyExactSecretInfo, error) {
	a.mu.RLock()
	defer a.mu.RUnlock()

	svc, err := a.privacyServiceOrError()
	if err != nil {
		return PrivacyExactSecretInfo{}, err
	}
	ctx, cancel := privacyAPIContext()
	defer cancel()
	updated, err := svc.UpdateExactSecret(ctx, id, privacyExactSecretRecordFromInput(id, input))
	if err != nil {
		return PrivacyExactSecretInfo{}, privacyExactSecretAPIError(err)
	}
	return buildPrivacyExactSecretInfo(updated), nil
}

// DeletePrivacyExactSecret 删除单条本地精确敏感值。
func (a *App) DeletePrivacyExactSecret(id int64) error {
	a.mu.RLock()
	defer a.mu.RUnlock()

	svc, err := a.privacyServiceOrError()
	if err != nil {
		return err
	}
	ctx, cancel := privacyAPIContext()
	defer cancel()
	return svc.DeleteExactSecret(ctx, id)
}

// ClearPrivacyExactSecrets 清空本地精确敏感值库，要求确认文案。
func (a *App) ClearPrivacyExactSecrets(confirmText string) error {
	a.mu.RLock()
	defer a.mu.RUnlock()

	if !strings.Contains(confirmText, "清空本地敏感值") {
		return fmt.Errorf("需要输入确认文案：清空本地敏感值")
	}
	svc, err := a.privacyServiceOrError()
	if err != nil {
		return err
	}
	ctx, cancel := privacyAPIContext()
	defer cancel()
	return svc.ClearExactSecrets(ctx)
}

// ListPrivacySecretImportCandidates 列出端点/账号中的可导入候选，不返回明文。
func (a *App) ListPrivacySecretImportCandidates() ([]PrivacySecretImportCandidateInfo, error) {
	a.mu.RLock()
	defer a.mu.RUnlock()

	svc, err := a.privacyServiceOrError()
	if err != nil {
		return nil, err
	}
	ctx, cancel := privacyAPIContext()
	defer cancel()
	existingHashes, err := a.existingPrivacyExactSecretHashes(ctx, svc)
	if err != nil {
		return nil, err
	}

	var out []PrivacySecretImportCandidateInfo
	if a.endpointService != nil {
		endpoints, err := a.endpointService.ListEndpoints(ctx)
		if err != nil {
			return nil, err
		}
		for _, endpoint := range endpoints {
			if endpoint == nil {
				continue
			}
			if value := strings.TrimSpace(endpoint.Token); value != "" {
				out = append(out, buildPrivacyImportCandidate(
					"endpoint_token", strconv.FormatInt(endpoint.ID, 10),
					fmt.Sprintf("端点 %s Token", endpoint.Name), "token", value, existingHashes,
				))
			}
			if value := strings.TrimSpace(endpoint.ApiKey); value != "" {
				out = append(out, buildPrivacyImportCandidate(
					"endpoint_api_key", strconv.FormatInt(endpoint.ID, 10),
					fmt.Sprintf("端点 %s API Key", endpoint.Name), "api_key", value, existingHashes,
				))
			}
		}
	}
	if a.accountPoolService != nil {
		accounts, err := a.accountPoolService.ListAccounts(ctx, true)
		if err != nil {
			return nil, err
		}
		for _, account := range accounts {
			if account == nil || !strings.EqualFold(strings.TrimSpace(account.ProviderType), "api_key") {
				continue
			}
			if value := strings.TrimSpace(account.CredentialRaw); value != "" {
				out = append(out, buildPrivacyImportCandidate(
					"upstream_account", strconv.FormatInt(account.ID, 10),
					fmt.Sprintf("账号 %s API Key", account.AccountName), "api_key", value, existingHashes,
				))
			}
		}
	}
	return out, nil
}

// ImportPrivacySecretCandidate 从候选来源复制一份明文到本地精确敏感值库。
func (a *App) ImportPrivacySecretCandidate(input ImportPrivacySecretCandidateInput) (PrivacyExactSecretInfo, error) {
	a.mu.RLock()
	defer a.mu.RUnlock()

	svc, err := a.privacyServiceOrError()
	if err != nil {
		return PrivacyExactSecretInfo{}, err
	}
	ctx, cancel := privacyAPIContext()
	defer cancel()
	secretValue, fallbackName, fallbackCategory, err := a.resolvePrivacyImportCandidateValue(ctx, input)
	if err != nil {
		return PrivacyExactSecretInfo{}, err
	}
	name := strings.TrimSpace(input.Name)
	if name == "" {
		name = fallbackName
	}
	category := strings.TrimSpace(input.Category)
	if category == "" {
		category = fallbackCategory
	}
	created, err := svc.CreateExactSecret(ctx, &store.PrivacyExactSecretRecord{
		Enabled:     true,
		Name:        name,
		SecretValue: secretValue,
		Category:    category,
		Placeholder: strings.TrimSpace(input.Placeholder),
		SourceType:  strings.TrimSpace(input.SourceType),
		SourceRef:   strings.TrimSpace(input.SourceRef),
		Description: strings.TrimSpace(input.Description),
	})
	if err != nil {
		return PrivacyExactSecretInfo{}, privacyExactSecretAPIError(err)
	}
	return buildPrivacyExactSecretInfo(created), nil
}

func (a *App) existingPrivacyExactSecretHashes(ctx context.Context, svc *service.PrivacyService) (map[string]struct{}, error) {
	records, err := svc.ListExactSecrets(ctx)
	if err != nil {
		return nil, err
	}
	out := make(map[string]struct{}, len(records))
	for _, record := range records {
		if record != nil && record.ValueHash != "" {
			out[record.ValueHash] = struct{}{}
		}
	}
	return out, nil
}

func (a *App) resolvePrivacyImportCandidateValue(ctx context.Context, input ImportPrivacySecretCandidateInput) (string, string, string, error) {
	sourceType := strings.TrimSpace(strings.ToLower(input.SourceType))
	sourceRef := strings.TrimSpace(input.SourceRef)
	if sourceType == "manual" {
		return strings.TrimSpace(input.SecretValue), "手动敏感值", strings.TrimSpace(input.Category), nil
	}
	sourceID, err := strconv.ParseInt(sourceRef, 10, 64)
	if err != nil || sourceID <= 0 {
		return "", "", "", fmt.Errorf("invalid source_ref")
	}
	switch sourceType {
	case "endpoint_token", "endpoint_api_key":
		if a.endpointService == nil {
			return "", "", "", fmt.Errorf("端点服务未启用")
		}
		endpoints, err := a.endpointService.ListEndpoints(ctx)
		if err != nil {
			return "", "", "", err
		}
		for _, endpoint := range endpoints {
			if endpoint == nil || endpoint.ID != sourceID {
				continue
			}
			if sourceType == "endpoint_token" {
				return strings.TrimSpace(endpoint.Token), fmt.Sprintf("端点 %s Token", endpoint.Name), "token", nil
			}
			return strings.TrimSpace(endpoint.ApiKey), fmt.Sprintf("端点 %s API Key", endpoint.Name), "api_key", nil
		}
	case "upstream_account":
		if a.accountPoolService == nil {
			return "", "", "", fmt.Errorf("账号池服务未启用")
		}
		account, err := a.accountPoolService.GetAccount(ctx, sourceID)
		if err != nil {
			return "", "", "", err
		}
		if account != nil && strings.EqualFold(strings.TrimSpace(account.ProviderType), "api_key") {
			return strings.TrimSpace(account.CredentialRaw), fmt.Sprintf("账号 %s API Key", account.AccountName), "api_key", nil
		}
	}
	return "", "", "", fmt.Errorf("未找到可导入的敏感值候选")
}

func buildPrivacyImportCandidate(sourceType, sourceRef, name, category, value string, existingHashes map[string]struct{}) PrivacySecretImportCandidateInfo {
	valueHash := service.HashPrivacySecretValue(value)
	_, exists := existingHashes[valueHash]
	return PrivacySecretImportCandidateInfo{
		SourceType:     sourceType,
		SourceRef:      sourceRef,
		Name:           name,
		Category:       category,
		MaskedValue:    service.MaskPrivacySecretValue(value, valueHash),
		ValueLength:    len(value),
		ValueHashShort: shortPrivacyValueHash(valueHash),
		AlreadyExists:  exists,
	}
}

func buildPrivacyExactSecretInfo(record *store.PrivacyExactSecretRecord) PrivacyExactSecretInfo {
	if record == nil {
		return PrivacyExactSecretInfo{}
	}
	return PrivacyExactSecretInfo{
		ID:             record.ID,
		Enabled:        record.Enabled,
		Name:           record.Name,
		Category:       record.Category,
		Placeholder:    record.Placeholder,
		SourceType:     record.SourceType,
		SourceRef:      record.SourceRef,
		Description:    record.Description,
		MaskedValue:    service.MaskPrivacySecretValue(record.SecretValue, record.ValueHash),
		ValueLength:    len(record.SecretValue),
		ValueHashShort: shortPrivacyValueHash(record.ValueHash),
		CreatedAt:      formatTime(record.CreatedAt),
		UpdatedAt:      formatTime(record.UpdatedAt),
	}
}

func privacyExactSecretRecordFromInput(id int64, input PrivacyExactSecretInput) *store.PrivacyExactSecretRecord {
	return &store.PrivacyExactSecretRecord{
		ID:          id,
		Enabled:     input.Enabled,
		Name:        strings.TrimSpace(input.Name),
		SecretValue: strings.TrimSpace(input.SecretValue),
		Category:    strings.TrimSpace(input.Category),
		Placeholder: strings.TrimSpace(input.Placeholder),
		SourceType:  strings.TrimSpace(input.SourceType),
		SourceRef:   strings.TrimSpace(input.SourceRef),
		Description: strings.TrimSpace(input.Description),
	}
}

func privacyExactSecretAPIError(err error) error {
	if errors.Is(err, service.ErrPrivacyExactSecretExists) {
		return fmt.Errorf(service.PrivacyExactSecretExistsCode)
	}
	return err
}

func shortPrivacyValueHash(valueHash string) string {
	if len(valueHash) <= 8 {
		return valueHash
	}
	return valueHash[:8]
}
