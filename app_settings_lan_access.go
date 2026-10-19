package main

import (
	"context"
	"fmt"
	"strings"

	"sinuo/internal/service"
)

// validateLANAccessSettingUpdates validates the effective post-save state so a
// batch can enable LAN access and set its token atomically.
func (a *App) validateLANAccessSettingUpdates(ctx context.Context, updates []UpdateSettingInput) error {
	if a == nil || a.settingsService == nil {
		return nil
	}

	lanAccess := a.settingsService.GetBool(ctx, service.CategoryServer, "lan_access", false)
	token, err := a.settingsService.GetValue(ctx, service.CategoryAuth, "token")
	if err != nil {
		return fmt.Errorf("读取访问鉴权设置失败: %w", err)
	}
	if strings.TrimSpace(token) == "" && a.config != nil {
		token = a.config.Auth.Token
	}

	for _, update := range updates {
		switch {
		case update.Category == service.CategoryServer && update.Key == "lan_access":
			lanAccess = service.ParseBool(update.Value, false)
		case update.Category == service.CategoryAuth && update.Key == "token":
			token = update.Value
		}
	}

	if lanAccess && strings.TrimSpace(token) == "" {
		return fmt.Errorf("开启局域网访问前请先在「访问鉴权」中设置 Token")
	}
	return nil
}
