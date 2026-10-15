package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"

	"sinuo/internal/privacy"
	"sinuo/internal/store"
)

const (
	PrivacyExactSecretExistsCode = "privacy_exact_secret_exists"

	exactSecretPriorityBase = -100000
)

var ErrPrivacyExactSecretExists = errors.New(PrivacyExactSecretExistsCode)

// HashPrivacySecretValue 返回精确敏感值的 sha256 hex，用于去重和审计展示。
func HashPrivacySecretValue(secretValue string) string {
	sum := sha256.Sum256([]byte(secretValue))
	return hex.EncodeToString(sum[:])
}

// MaskPrivacySecretValue 生成前端可展示的掩码，不泄露短敏感值片段或内部 hash 信息。
func MaskPrivacySecretValue(secretValue, _ string) string {
	length := len(secretValue)
	switch {
	case length <= 0:
		return ""
	case length < 16:
		return "••••••••"
	case length < 32:
		return fmt.Sprintf("%s…%s", secretValue[:2], secretValue[length-2:])
	default:
		return fmt.Sprintf("%s…%s", secretValue[:6], secretValue[length-4:])
	}
}

// ListExactSecrets 列出本地精确敏感值。返回值含 SecretValue，API 层必须遮蔽后再返回前端。
func (s *PrivacyService) ListExactSecrets(ctx context.Context) ([]*store.PrivacyExactSecretRecord, error) {
	if s == nil || s.store == nil {
		return nil, fmt.Errorf("privacy service is not initialized")
	}
	return s.store.ListExactSecrets(ctx)
}

// GetExactSecret 读取单条精确敏感值。
func (s *PrivacyService) GetExactSecret(ctx context.Context, id int64) (*store.PrivacyExactSecretRecord, error) {
	if s == nil || s.store == nil {
		return nil, fmt.Errorf("privacy service is not initialized")
	}
	return s.store.GetExactSecret(ctx, id)
}

// ExactSecretExists 判断一段明文是否已登记。
func (s *PrivacyService) ExactSecretExists(ctx context.Context, secretValue string) (bool, error) {
	if s == nil || s.store == nil {
		return false, fmt.Errorf("privacy service is not initialized")
	}
	secretValue = strings.TrimSpace(secretValue)
	if secretValue == "" {
		return false, nil
	}
	record, err := s.store.FindExactSecretByHash(ctx, HashPrivacySecretValue(secretValue))
	if err != nil {
		return false, err
	}
	return record != nil, nil
}

// CreateExactSecret 新增本地精确敏感值并热生效。
func (s *PrivacyService) CreateExactSecret(ctx context.Context, record *store.PrivacyExactSecretRecord) (*store.PrivacyExactSecretRecord, error) {
	if record == nil {
		return nil, fmt.Errorf("privacy exact secret is nil")
	}
	normalized, err := normalizeExactSecretRecord(record, true)
	if err != nil {
		return nil, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.ensureExactSecretHashAvailable(ctx, normalized.ValueHash, 0); err != nil {
		return nil, err
	}
	created, err := s.store.CreateExactSecret(ctx, normalized)
	if err != nil {
		return nil, mapExactSecretStoreError(err)
	}
	if err := s.rebuildSnapshotLocked(ctx, false); err != nil {
		return nil, err
	}
	return created, nil
}

// UpdateExactSecret 更新精确敏感值；SecretValue 为空时只更新元数据。
func (s *PrivacyService) UpdateExactSecret(ctx context.Context, id int64, input *store.PrivacyExactSecretRecord) (*store.PrivacyExactSecretRecord, error) {
	if id <= 0 || input == nil {
		return nil, fmt.Errorf("invalid privacy exact secret")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	existing, err := s.store.GetExactSecret(ctx, id)
	if err != nil {
		return nil, err
	}
	if existing == nil {
		return nil, fmt.Errorf("privacy exact secret %d not found", id)
	}

	next := *existing
	next.Enabled = input.Enabled
	next.Name = input.Name
	next.Placeholder = input.Placeholder
	next.Category = input.Category
	next.SourceType = input.SourceType
	next.SourceRef = input.SourceRef
	next.Description = input.Description
	if strings.TrimSpace(input.SecretValue) != "" {
		next.SecretValue = input.SecretValue
	}
	normalized, err := normalizeExactSecretRecord(&next, false)
	if err != nil {
		return nil, err
	}
	if err := s.ensureExactSecretHashAvailable(ctx, normalized.ValueHash, id); err != nil {
		return nil, err
	}
	if err := s.store.UpdateExactSecret(ctx, normalized); err != nil {
		return nil, mapExactSecretStoreError(err)
	}
	if err := s.rebuildSnapshotLocked(ctx, false); err != nil {
		return nil, err
	}
	return s.store.GetExactSecret(ctx, id)
}

// DeleteExactSecret 删除精确敏感值并热生效。
func (s *PrivacyService) DeleteExactSecret(ctx context.Context, id int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.store.DeleteExactSecret(ctx, id); err != nil {
		return err
	}
	return s.rebuildSnapshotLocked(ctx, false)
}

// ClearExactSecrets 清空本地精确敏感值库并热生效。
func (s *PrivacyService) ClearExactSecrets(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.store.ClearExactSecrets(ctx); err != nil {
		return err
	}
	return s.rebuildSnapshotLocked(ctx, false)
}

func normalizeExactSecretRecord(record *store.PrivacyExactSecretRecord, requireValue bool) (*store.PrivacyExactSecretRecord, error) {
	next := *record
	next.Name = strings.TrimSpace(next.Name)
	next.SecretValue = strings.TrimSpace(next.SecretValue)
	next.Placeholder = strings.TrimSpace(next.Placeholder)
	next.Category = normalizeExactSecretCategory(next.Category)
	next.SourceType = normalizeExactSecretSourceType(next.SourceType)
	next.SourceRef = strings.TrimSpace(next.SourceRef)
	next.Description = strings.TrimSpace(next.Description)
	if next.Name == "" {
		next.Name = "本地敏感值"
	}
	if next.Placeholder == "" {
		next.Placeholder = "[敏感值]"
	}
	if requireValue && next.SecretValue == "" {
		return nil, fmt.Errorf("secret_value is required")
	}
	if next.SecretValue == "" {
		return nil, fmt.Errorf("secret_value is required")
	}
	if minLength := exactSecretMinLength(next.Category); len(next.SecretValue) < minLength {
		return nil, fmt.Errorf("secret_value too short for category %s: min_length=%d", next.Category, minLength)
	}
	next.ValueHash = HashPrivacySecretValue(next.SecretValue)
	return &next, nil
}

func normalizeExactSecretCategory(category string) string {
	switch strings.ToLower(strings.TrimSpace(category)) {
	case "api_key", "token", "password", "custom":
		return strings.ToLower(strings.TrimSpace(category))
	default:
		return "custom"
	}
}

func normalizeExactSecretSourceType(sourceType string) string {
	switch strings.ToLower(strings.TrimSpace(sourceType)) {
	case "manual", "endpoint_token", "endpoint_api_key", "upstream_account", "imported":
		return strings.ToLower(strings.TrimSpace(sourceType))
	default:
		return "manual"
	}
}

func exactSecretMinLength(category string) int {
	switch category {
	case "api_key", "token":
		return 12
	case "password":
		return 8
	default:
		return 4
	}
}

func (s *PrivacyService) ensureExactSecretHashAvailable(ctx context.Context, valueHash string, ignoreID int64) error {
	existing, err := s.store.FindExactSecretByHash(ctx, valueHash)
	if err != nil {
		return err
	}
	if existing != nil && existing.ID != ignoreID {
		return ErrPrivacyExactSecretExists
	}
	return nil
}

func mapExactSecretStoreError(err error) error {
	if err == nil {
		return nil
	}
	if strings.Contains(strings.ToLower(err.Error()), "unique") &&
		strings.Contains(strings.ToLower(err.Error()), "value_hash") {
		return ErrPrivacyExactSecretExists
	}
	return err
}

func exactSecretRules(records []*store.PrivacyExactSecretRecord) []privacy.Rule {
	enabled := make([]*store.PrivacyExactSecretRecord, 0, len(records))
	for _, record := range records {
		if record == nil || !record.Enabled || strings.TrimSpace(record.SecretValue) == "" {
			continue
		}
		enabled = append(enabled, record)
	}
	sort.SliceStable(enabled, func(i, j int) bool {
		if len(enabled[i].SecretValue) != len(enabled[j].SecretValue) {
			return len(enabled[i].SecretValue) > len(enabled[j].SecretValue)
		}
		return enabled[i].ID < enabled[j].ID
	})

	rules := make([]privacy.Rule, 0, len(enabled))
	for i, record := range enabled {
		placeholder := record.Placeholder
		if placeholder == "" {
			placeholder = "[敏感值]"
		}
		name := record.Name
		if name == "" {
			name = "本地敏感值"
		}
		rules = append(rules, privacy.Rule{
			ID:          exactRuleID(record.ID),
			Enabled:     true,
			Name:        name,
			Description: record.Description,
			Priority:    exactSecretPriorityBase + i,
			MatchType:   privacy.MatchTypeLiteral,
			Pattern:     record.SecretValue,
			Placeholder: placeholder,
			Action:      privacy.ActionRedact,
			Source:      privacy.SourceExact,
		})
	}
	return rules
}

func exactRuleID(secretID int64) int64 {
	if secretID <= 0 {
		return 0
	}
	return -secretID
}
