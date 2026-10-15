package main

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"sinuo/internal/proxy"
	"sinuo/internal/service"
	"sinuo/internal/transport"
)

type imageGenerationConfigProvider struct {
	app *App
}

func (p *imageGenerationConfigProvider) GetImageGenerationConfig(ctx context.Context) (proxy.ImageGenerationConfig, error) {
	if p == nil || p.app == nil || p.app.settingsService == nil {
		return proxy.ImageGenerationConfig{}, nil
	}
	settings := p.app.settingsService
	directConnect := settings.GetBool(ctx, service.CategoryImageGeneration, "direct_connect", false)
	var directPortMin, directPortMax int
	if directConnect {
		var err error
		directPortMin, directPortMax, err = transport.ParseSourcePortRange(p.app.getSettingString(ctx, service.CategoryImageGeneration, "direct_source_port_range", "31080-31179"))
		if err != nil {
			return proxy.ImageGenerationConfig{}, err
		}
	}
	return proxy.ImageGenerationConfig{
		Enabled:       settings.GetBool(ctx, service.CategoryImageGeneration, "enabled", false),
		DirectConnect: directConnect,
		DirectPortMin: directPortMin,
		DirectPortMax: directPortMax,
		EndpointURL:   strings.TrimSpace(p.app.getSettingString(ctx, service.CategoryImageGeneration, "endpoint_url", "")),
		APIKey:        strings.TrimSpace(p.app.getSettingString(ctx, service.CategoryImageGeneration, "api_key", "")),
		Model:         strings.TrimSpace(p.app.getSettingString(ctx, service.CategoryImageGeneration, "model", "gpt-image-2")),
		FixedPriceUSD: settings.GetFloat(ctx, service.CategoryImageGeneration, "fixed_price_usd", 0),
		Timeout:       settings.GetDuration(ctx, service.CategoryImageGeneration, "timeout", 300*time.Second),
	}, nil
}

func (a *App) validateImageGenerationSettingUpdates(ctx context.Context, updates []UpdateSettingInput) error {
	if a == nil || a.settingsService == nil {
		return nil
	}
	values := map[string]string{}
	for _, key := range []string{"enabled", "direct_connect", "direct_source_port_range", "endpoint_url", "api_key", "model", "fixed_price_usd", "timeout"} {
		value, err := a.settingsService.GetValue(ctx, service.CategoryImageGeneration, key)
		if err != nil {
			return fmt.Errorf("读取图像生成设置失败: %w", err)
		}
		values[key] = value
	}
	changed := false
	for _, update := range updates {
		if update.Category != service.CategoryImageGeneration {
			continue
		}
		changed = true
		if update.Key == "api_key" && update.Value == "" {
			continue
		}
		values[update.Key] = strings.TrimSpace(update.Value)
	}
	if !changed {
		return nil
	}

	if raw := values["endpoint_url"]; raw != "" {
		parsed, err := url.Parse(raw)
		if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.User != nil {
			return fmt.Errorf("图像生成完整请求 URL 必须是有效的 http/https 地址")
		}
	}
	if raw := values["timeout"]; raw != "" {
		timeout, err := time.ParseDuration(raw)
		if err != nil || timeout <= 0 || timeout > 30*time.Minute {
			return fmt.Errorf("图像生成请求超时必须在 0 到 30 分钟之间，例如 300s")
		}
	}
	if _, _, err := transport.ParseSourcePortRange(values["direct_source_port_range"]); err != nil {
		return err
	}
	if values["model"] == "" {
		return fmt.Errorf("图像生成默认模型不能为空")
	}
	if raw := values["fixed_price_usd"]; raw != "" {
		price, err := strconv.ParseFloat(raw, 64)
		if err != nil || price < 0 || price > 1000 {
			return fmt.Errorf("图像生成固定价格必须是 0 到 1000 之间的美元金额")
		}
	}
	enabled := values["enabled"] == "true" || values["enabled"] == "1" || values["enabled"] == "yes"
	if enabled {
		if values["endpoint_url"] == "" {
			return fmt.Errorf("启用图像生成前必须填写完整请求 URL")
		}
		if values["api_key"] == "" {
			return fmt.Errorf("启用图像生成前必须填写 API Key")
		}
	}
	return nil
}
