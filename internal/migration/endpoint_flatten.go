package migration

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"cc-forwarder/config"
)

type EndpointSnapshot struct {
	ID                            int64
	Name                          string
	URL                           string
	Token                         string
	APIKey                        string
	Headers                       map[string]string
	Priority                      int
	FailoverEnabled               bool
	AvailabilityEnabled           bool
	CooldownSeconds               *int64
	TimeoutSeconds                int64
	SupportsCountTokens           bool
	ModelRewriteRules             string
	SoftFailureWindowMinutes      *int
	SoftFailureThreshold          *int
	CostMultiplier                float64
	InputCostMultiplier           float64
	OutputCostMultiplier          float64
	CacheCreationCostMultiplier   float64
	CacheCreationCostMultiplier1h float64
	CacheReadCostMultiplier       float64
	CreatedAt                     string
	UpdatedAt                     string
	SourceName                    string
	TokenLabel                    string
	APIKeyLabel                   string
	Derived                       bool
}

type EndpointFlattenResult struct {
	Endpoints            []EndpointSnapshot
	DerivedNamesBySource map[string][]string
	SourceEndpointCount  int
	SplitEndpointCount   int
	DerivedRecordCount   int
}

type credentialCandidate struct {
	Label string
	Value string
}

type resolvedLegacyEndpoint struct {
	LegacyEndpoint
	Group          string
	GroupPriority  int
	Timeout        time.Duration
	Headers        map[string]string
	ResolvedToken  string
	ResolvedAPIKey string
}

func FlattenLegacyEndpoints(legacy *LegacyConfig) (EndpointFlattenResult, error) {
	if legacy == nil {
		return EndpointFlattenResult{}, fmt.Errorf("legacy config is nil")
	}
	resolved := resolveLegacyEndpoints(legacy)
	usedNames := make(map[string]struct{}, len(resolved))
	for _, source := range resolved {
		baseName := cleanName(source.Name)
		if baseName == "" {
			return EndpointFlattenResult{}, fmt.Errorf("legacy endpoint name is empty")
		}
		if _, exists := usedNames[baseName]; exists {
			return EndpointFlattenResult{}, fmt.Errorf("duplicate legacy endpoint name %q", baseName)
		}
		usedNames[baseName] = struct{}{}
	}
	result := EndpointFlattenResult{
		Endpoints:            make([]EndpointSnapshot, 0, len(resolved)),
		DerivedNamesBySource: make(map[string][]string),
		SourceEndpointCount:  len(resolved),
	}

	for _, source := range resolved {
		baseName := cleanName(source.Name)
		if baseName == "" {
			return EndpointFlattenResult{}, fmt.Errorf("legacy endpoint name is empty")
		}
		if strings.TrimSpace(source.URL) == "" {
			return EndpointFlattenResult{}, fmt.Errorf("legacy endpoint %q URL is empty", baseName)
		}
		tokens := tokenCandidates(source)
		apiKeys := apiKeyCandidates(source)
		combinationCount := len(tokens) * len(apiKeys)
		if combinationCount > 1 {
			result.SplitEndpointCount++
		}
		combinationIndex := 0
		for _, token := range tokens {
			for _, apiKey := range apiKeys {
				name := baseName
				derived := combinationIndex > 0
				if derived {
					name = derivedEndpointName(baseName, token.Label, apiKey.Label, usedNames)
					result.DerivedNamesBySource[baseName] = append(result.DerivedNamesBySource[baseName], name)
					result.DerivedRecordCount++
				}

				timeout := source.Timeout
				if timeout <= 0 {
					timeout = 300 * time.Second
				}
				var cooldownSeconds *int64
				if source.Cooldown != nil && source.Cooldown.Duration > 0 {
					seconds := int64(source.Cooldown.Duration / time.Second)
					cooldownSeconds = &seconds
				}
				result.Endpoints = append(result.Endpoints, EndpointSnapshot{
					Name:                          name,
					URL:                           strings.TrimSpace(source.URL),
					Token:                         token.Value,
					APIKey:                        apiKey.Value,
					Headers:                       cloneHeaders(source.Headers),
					Priority:                      max(source.Priority, 0),
					FailoverEnabled:               !derived && boolDefault(source.FailoverEnabled, true),
					AvailabilityEnabled:           !derived && boolDefault(source.AvailabilityEnabled, true),
					CooldownSeconds:               cooldownSeconds,
					TimeoutSeconds:                int64(timeout / time.Second),
					SupportsCountTokens:           source.SupportsCountTokens,
					ModelRewriteRules:             strings.TrimSpace(source.ModelRewriteRules),
					SoftFailureWindowMinutes:      config.CloneSoftFailureValue(source.SoftFailureWindowMinutes),
					SoftFailureThreshold:          config.CloneSoftFailureValue(source.SoftFailureThreshold),
					CostMultiplier:                positiveOrDefault(source.CostMultiplier, 1),
					InputCostMultiplier:           positiveOrDefault(source.InputCostMultiplier, 1),
					OutputCostMultiplier:          positiveOrDefault(source.OutputCostMultiplier, 1),
					CacheCreationCostMultiplier:   positiveOrDefault(source.CacheCreationCostMultiplier, 1),
					CacheCreationCostMultiplier1h: positiveOrDefault(source.CacheCreationCostMultiplier1h, 1),
					CacheReadCostMultiplier:       positiveOrDefault(source.CacheReadCostMultiplier, 1),
					SourceName:                    baseName,
					TokenLabel:                    token.Label,
					APIKeyLabel:                   apiKey.Label,
					Derived:                       derived,
				})
				combinationIndex++
			}
		}
	}
	return result, nil
}

func resolveLegacyEndpoints(legacy *LegacyConfig) []resolvedLegacyEndpoint {
	resolved := make([]resolvedLegacyEndpoint, len(legacy.Endpoints))
	currentGroup := "Default"
	currentGroupPriority := 1
	globalTimeout := legacy.GlobalTimeout
	if globalTimeout <= 0 {
		globalTimeout = 300 * time.Second
	}

	for i, source := range legacy.Endpoints {
		item := resolvedLegacyEndpoint{LegacyEndpoint: source}
		if item.Priority == 0 && item.LegacyEndpoint.GroupPriority > 0 {
			item.Priority = item.LegacyEndpoint.GroupPriority
		}
		if strings.TrimSpace(source.Group) != "" {
			currentGroup = strings.TrimSpace(source.Group)
			if source.GroupPriority != 0 {
				currentGroupPriority = source.GroupPriority
			}
		}
		item.Group = currentGroup
		item.GroupPriority = source.GroupPriority
		if item.GroupPriority == 0 {
			item.GroupPriority = currentGroupPriority
		}

		item.Timeout = source.Timeout.Duration
		if item.Timeout <= 0 {
			if i > 0 && resolved[0].Timeout > 0 {
				item.Timeout = resolved[0].Timeout
			} else {
				item.Timeout = globalTimeout
			}
		}
		item.Headers = cloneHeaders(source.Headers)
		if i > 0 && len(resolved[0].Headers) > 0 {
			merged := cloneHeaders(resolved[0].Headers)
			for key, value := range item.Headers {
				merged[key] = value
			}
			item.Headers = merged
		}
		item.ResolvedAPIKey = source.APIKey
		if item.ResolvedAPIKey == "" && i > 0 && resolved[0].ResolvedAPIKey != "" {
			item.ResolvedAPIKey = resolved[0].ResolvedAPIKey
		}
		item.ResolvedToken = source.Token
		resolved[i] = item
	}

	for i := range resolved {
		if len(resolved[i].Tokens) == 0 && resolved[i].ResolvedToken == "" {
			for _, candidate := range resolved {
				if candidate.Group == resolved[i].Group && candidate.Token != "" {
					resolved[i].ResolvedToken = candidate.Token
					break
				}
			}
		}
		if len(resolved[i].APIKeys) == 0 && resolved[i].ResolvedAPIKey == "" {
			for _, candidate := range resolved {
				if candidate.Group == resolved[i].Group && candidate.ResolvedAPIKey != "" {
					resolved[i].ResolvedAPIKey = candidate.ResolvedAPIKey
					break
				}
			}
		}
	}
	return resolved
}

func tokenCandidates(endpoint resolvedLegacyEndpoint) []credentialCandidate {
	if len(endpoint.Tokens) > 0 {
		result := make([]credentialCandidate, 0, len(endpoint.Tokens))
		for i, token := range endpoint.Tokens {
			result = append(result, credentialCandidate{
				Label: normalizedCredentialLabel(token.Name, "token", i+1),
				Value: token.Value,
			})
		}
		return result
	}
	return []credentialCandidate{{Value: endpoint.ResolvedToken}}
}

func apiKeyCandidates(endpoint resolvedLegacyEndpoint) []credentialCandidate {
	if len(endpoint.APIKeys) > 0 {
		result := make([]credentialCandidate, 0, len(endpoint.APIKeys))
		for i, apiKey := range endpoint.APIKeys {
			result = append(result, credentialCandidate{
				Label: normalizedCredentialLabel(apiKey.Name, "api-key", i+1),
				Value: apiKey.Value,
			})
		}
		return result
	}
	return []credentialCandidate{{Value: endpoint.ResolvedAPIKey}}
}

var nameWhitespace = regexp.MustCompile(`\s+`)

func cleanName(value string) string {
	value = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, value)
	return strings.TrimSpace(nameWhitespace.ReplaceAllString(value, " "))
}

func normalizedCredentialLabel(label, fallbackPrefix string, position int) string {
	label = cleanName(label)
	if label == "" {
		return fmt.Sprintf("%s-%d", fallbackPrefix, position)
	}
	return label
}

func derivedEndpointName(baseName, tokenLabel, apiKeyLabel string, used map[string]struct{}) string {
	parts := []string{baseName}
	if tokenLabel != "" {
		parts = append(parts, tokenLabel)
	}
	if apiKeyLabel != "" {
		parts = append(parts, apiKeyLabel)
	}
	candidate := strings.Join(parts, " · ")
	if _, exists := used[candidate]; !exists {
		used[candidate] = struct{}{}
		return candidate
	}
	for suffix := 2; ; suffix++ {
		unique := fmt.Sprintf("%s-%d", candidate, suffix)
		if _, exists := used[unique]; !exists {
			used[unique] = struct{}{}
			return unique
		}
	}
}

func loadSQLiteEndpointSnapshots(ctx context.Context, tx *sql.Tx) (EndpointFlattenResult, error) {
	exists, err := tableExistsTx(ctx, tx, "endpoints")
	if err != nil || !exists {
		return EndpointFlattenResult{DerivedNamesBySource: map[string][]string{}}, err
	}
	columns, err := tableColumnsTx(ctx, tx, "endpoints")
	if err != nil {
		return EndpointFlattenResult{}, err
	}
	expr := func(name, fallback string) string {
		if columns[name] {
			return quoteIdentifier(name)
		}
		return fallback
	}
	query := `SELECT ` + strings.Join([]string{
		expr("id", "0"), expr("name", "''"), expr("url", "''"),
		expr("token", "''"), expr("api_key", "''"), expr("headers", "'{}'"),
		expr("priority", "1"), expr("failover_enabled", "1"), expr("availability_enabled", "1"),
		expr("cooldown_seconds", "NULL"), expr("timeout_seconds", "300"), expr("supports_count_tokens", "0"),
		expr("model_rewrite_rules", "''"), expr("soft_failure_window_minutes", "NULL"), expr("soft_failure_threshold", "NULL"), expr("cost_multiplier", "1.0"),
		expr("input_cost_multiplier", "1.0"), expr("output_cost_multiplier", "1.0"),
		expr("cache_creation_cost_multiplier", "1.0"), expr("cache_creation_cost_multiplier_1h", "1.0"),
		expr("cache_read_cost_multiplier", "1.0"), expr("created_at", "strftime('%Y-%m-%dT%H:%M:%f000Z', 'now')"),
		expr("updated_at", "strftime('%Y-%m-%dT%H:%M:%f000Z', 'now')"),
	}, ", ") + ` FROM endpoints ORDER BY id ASC`
	rows, err := tx.QueryContext(ctx, query)
	if err != nil {
		return EndpointFlattenResult{}, fmt.Errorf("read legacy SQLite endpoints: %w", err)
	}
	defer rows.Close()
	result := EndpointFlattenResult{DerivedNamesBySource: map[string][]string{}}
	seenNames := make(map[string]struct{})
	for rows.Next() {
		var snapshot EndpointSnapshot
		var headersJSON string
		var failover, availability, supports int
		var cooldown sql.NullInt64
		if err := rows.Scan(
			&snapshot.ID, &snapshot.Name, &snapshot.URL, &snapshot.Token, &snapshot.APIKey, &headersJSON,
			&snapshot.Priority, &failover, &availability, &cooldown, &snapshot.TimeoutSeconds, &supports,
			&snapshot.ModelRewriteRules, &snapshot.SoftFailureWindowMinutes, &snapshot.SoftFailureThreshold, &snapshot.CostMultiplier, &snapshot.InputCostMultiplier,
			&snapshot.OutputCostMultiplier, &snapshot.CacheCreationCostMultiplier,
			&snapshot.CacheCreationCostMultiplier1h, &snapshot.CacheReadCostMultiplier,
			&snapshot.CreatedAt, &snapshot.UpdatedAt,
		); err != nil {
			return EndpointFlattenResult{}, fmt.Errorf("scan legacy SQLite endpoint: %w", err)
		}
		snapshot.Name = cleanName(snapshot.Name)
		if snapshot.Name == "" || strings.TrimSpace(snapshot.URL) == "" {
			return EndpointFlattenResult{}, fmt.Errorf("legacy SQLite endpoint %d has empty name or URL", snapshot.ID)
		}
		if _, duplicate := seenNames[snapshot.Name]; duplicate {
			return EndpointFlattenResult{}, fmt.Errorf("duplicate SQLite endpoint name %q", snapshot.Name)
		}
		seenNames[snapshot.Name] = struct{}{}
		if strings.TrimSpace(headersJSON) == "" {
			headersJSON = "{}"
		}
		if err := json.Unmarshal([]byte(headersJSON), &snapshot.Headers); err != nil {
			return EndpointFlattenResult{}, fmt.Errorf("endpoint %q headers are invalid JSON: %w", snapshot.Name, err)
		}
		if snapshot.Headers == nil {
			snapshot.Headers = map[string]string{}
		}
		if cooldown.Valid {
			value := cooldown.Int64
			snapshot.CooldownSeconds = &value
		}
		snapshot.FailoverEnabled = failover != 0
		snapshot.AvailabilityEnabled = availability != 0
		snapshot.SupportsCountTokens = supports != 0
		snapshot.CostMultiplier = positiveOrDefault(snapshot.CostMultiplier, 1)
		snapshot.InputCostMultiplier = positiveOrDefault(snapshot.InputCostMultiplier, 1)
		snapshot.OutputCostMultiplier = positiveOrDefault(snapshot.OutputCostMultiplier, 1)
		snapshot.CacheCreationCostMultiplier = positiveOrDefault(snapshot.CacheCreationCostMultiplier, 1)
		snapshot.CacheCreationCostMultiplier1h = positiveOrDefault(snapshot.CacheCreationCostMultiplier1h, 1)
		snapshot.CacheReadCostMultiplier = positiveOrDefault(snapshot.CacheReadCostMultiplier, 1)
		if snapshot.TimeoutSeconds <= 0 {
			snapshot.TimeoutSeconds = 300
		}
		snapshot.SourceName = snapshot.Name
		result.Endpoints = append(result.Endpoints, snapshot)
	}
	if err := rows.Err(); err != nil {
		return EndpointFlattenResult{}, err
	}
	result.SourceEndpointCount = len(result.Endpoints)
	return result, nil
}

func cloneHeaders(source map[string]string) map[string]string {
	if len(source) == 0 {
		return map[string]string{}
	}
	result := make(map[string]string, len(source))
	for key, value := range source {
		result[key] = value
	}
	return result
}

func sortedUnique(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}
