package main

import (
	"context"
	"encoding/json"
	"testing"
)

func TestAccountMetadataCompatibility_APIOptionalSetting(t *testing.T) {
	for _, provider := range []string{"api_key", "chatgpt_refresh_token"} {
		for _, initial := range []struct {
			name  string
			value any
			omit  bool
			want  bool
		}{
			{name: "省略默认关闭", omit: true},
			{name: "null默认关闭"},
			{name: "显式关闭", value: false},
			{name: "显式开启", value: true, want: true},
		} {
			t.Run(provider+"/"+initial.name, func(t *testing.T) {
				app, _ := newAccountPoolAPITestApp(t)
				inputFor := func(value any, omit bool) CreateUpstreamAccountInput {
					t.Helper()
					credential := "fixture-credential"
					if provider == "chatgpt_refresh_token" {
						credential = `{"refresh_token":"fixture-refresh-token"}`
					}
					payload := map[string]any{
						"provider_type": provider, "account_name": "metadata-api", "credential_raw": credential,
						"base_url": "http://127.0.0.1:1", "priority": 10, "enabled": true,
					}
					if !omit {
						payload["strip_codex_internal_metadata"] = value
					}
					raw, err := json.Marshal(payload)
					if err != nil {
						t.Fatal(err)
					}
					var input CreateUpstreamAccountInput
					if err := json.Unmarshal(raw, &input); err != nil {
						t.Fatal(err)
					}
					return input
				}
				assertSetting := func(want bool) int64 {
					t.Helper()
					accounts, err := app.GetUpstreamAccounts()
					if err != nil || len(accounts) != 1 {
						t.Fatalf("get accounts: count=%d err=%v", len(accounts), err)
					}
					if accounts[0].StripCodexInternalMetadata != want {
						t.Fatalf("API setting = %v, want %v", accounts[0].StripCodexInternalMetadata, want)
					}
					persisted, err := app.accountPoolStore.GetAccount(context.Background(), accounts[0].ID)
					if err != nil || persisted.StripCodexInternalMetadata != want {
						t.Fatalf("persisted setting mismatch: %v", err)
					}
					return accounts[0].ID
				}
				if err := app.CreateUpstreamAccount(inputFor(initial.value, initial.omit)); err != nil {
					t.Fatal(err)
				}
				id := assertSetting(initial.want)
				for _, edit := range []struct {
					value any
					omit  bool
					want  bool
				}{
					{omit: true, want: initial.want},
					{want: initial.want},
					{value: true, want: true},
					{omit: true, want: true},
					{want: true},
					{value: false},
				} {
					if err := app.UpdateUpstreamAccount(id, inputFor(edit.value, edit.omit)); err != nil {
						t.Fatal(err)
					}
					assertSetting(edit.want)
				}
			})
		}
	}
}
