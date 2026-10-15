package service

import (
	"context"
	"strings"

	"sinuo/internal/privacy"
	"sinuo/internal/store"
)

const retiredBuiltinEmailDescription = "已停用：邮箱格式误报风险高，请按需创建自定义规则"

func (s *PrivacyService) ensureBuiltinRulesLocked(ctx context.Context, existing []*store.PrivacyRuleRecord) error {
	existingBuiltin := make(map[string]struct{}, len(existing))
	for _, record := range existing {
		if record == nil {
			continue
		}
		if record.Source == privacy.SourceBuiltin && record.MatchType == privacy.MatchTypeBuiltin {
			existingBuiltin[record.Pattern] = struct{}{}
		}
	}

	var missing []*store.PrivacyRuleRecord
	for _, rule := range privacy.BuiltinPIIRules() {
		if _, ok := existingBuiltin[rule.Pattern]; ok {
			continue
		}
		scopeJSON, err := privacy.EncodeScope(rule.Scope)
		if err != nil {
			return err
		}
		missing = append(missing, &store.PrivacyRuleRecord{
			Enabled:     rule.Enabled,
			Name:        rule.Name,
			Description: rule.Description,
			Priority:    rule.Priority,
			MatchType:   rule.MatchType,
			Pattern:     rule.Pattern,
			Placeholder: rule.Placeholder,
			Action:      rule.Action,
			ScopeJSON:   scopeJSON,
			Source:      privacy.SourceBuiltin,
		})
	}
	if len(missing) == 0 {
		return nil
	}
	if _, err := s.store.CreateRules(ctx, missing); err != nil {
		return err
	}
	return nil
}

func (s *PrivacyService) retireLegacyBuiltinRulesLocked(ctx context.Context, existing []*store.PrivacyRuleRecord) error {
	for _, record := range existing {
		if record == nil || record.Source != privacy.SourceBuiltin ||
			record.MatchType != privacy.MatchTypeBuiltin || record.Pattern != privacy.BuiltinEmail {
			continue
		}
		if !record.Enabled && record.Description == retiredBuiltinEmailDescription {
			continue
		}
		updated := *record
		updated.Enabled = false
		if updated.Description == "" || strings.Contains(updated.Description, "邮箱") {
			updated.Description = retiredBuiltinEmailDescription
		}
		if err := s.store.UpdateRule(ctx, &updated); err != nil {
			return err
		}
	}
	return nil
}
