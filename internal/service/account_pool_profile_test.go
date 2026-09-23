package service

import (
	"compress/gzip"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cc-forwarder/internal/accountauth"
	"cc-forwarder/internal/store"

	_ "modernc.org/sqlite"
)

func TestRefreshAccountProfile_FreeAccountUsesWeeklyQuotaOnly(t *testing.T) {
	authServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token":  "at-free",
			"refresh_token": "rt-free-rotated",
			"expires_in":    3600,
			"id_token":      testServiceIDTokenWithProfile("free", "acc-free", "user-free", "org-free"),
		})
	}))
	defer authServer.Close()

	var receivedAccountHeader string
	quotaServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedAccountHeader = r.Header.Get("chatgpt-account-id")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"plan_type": "free",
			"rate_limit": map[string]any{
				"primary_window": map[string]any{
					"used_percent":        12.5,
					"reset_after_seconds": 600,
				},
			},
		})
	}))
	defer quotaServer.Close()

	svc, st, accountID := newOAuthProfileTestService(t, accountauth.BuildStoredOpenAICredential(
		"rt-free",
		"",
		"",
		accountauth.OpenAIAccountProfile{},
		time.Time{},
	))

	oldRefreshURL := accountauthOpenAIRefreshURL()
	oldQuotaURL := chatGPTWhamUsageURL
	t.Cleanup(func() {
		setAccountauthOpenAIRefreshURL(oldRefreshURL)
		chatGPTWhamUsageURL = oldQuotaURL
	})
	setAccountauthOpenAIRefreshURL(authServer.URL)
	chatGPTWhamUsageURL = quotaServer.URL

	result, err := svc.RefreshAccountProfile(context.Background(), accountID)
	if err != nil {
		t.Fatalf("RefreshAccountProfile failed: %v", err)
	}
	if !result.Success || result.QuotaStatus != quotaStatusOK {
		t.Fatalf("unexpected refresh result: %+v", result)
	}

	rec, err := st.GetAccount(context.Background(), accountID)
	if err != nil {
		t.Fatalf("GetAccount failed: %v", err)
	}
	if rec.PlanType != "free" {
		t.Fatalf("unexpected plan type: %s", rec.PlanType)
	}
	if rec.ChatGPTAccountID != "acc-free" || rec.ChatGPTUserID != "user-free" || rec.OrganizationID != "org-free" {
		t.Fatalf("unexpected profile: %+v", rec)
	}
	if rec.Quota5HUsedPercent != nil || rec.Quota5HResetAt != nil {
		t.Fatalf("free account should not have 5h quota: %+v", rec)
	}
	if rec.QuotaWeeklyUsedPercent == nil || *rec.QuotaWeeklyUsedPercent != 12.5 {
		t.Fatalf("unexpected weekly quota: %+v", rec.QuotaWeeklyUsedPercent)
	}
	if rec.QuotaStatus != quotaStatusOK || rec.QuotaRefreshedAt == nil {
		t.Fatalf("unexpected quota status: %+v", rec)
	}
	if !strings.Contains(rec.CredentialRaw, `"refresh_token":"rt-free-rotated"`) {
		t.Fatalf("credential should persist rotated refresh token: %s", rec.CredentialRaw)
	}
	if receivedAccountHeader != "" {
		t.Fatalf("free account should not send chatgpt-account-id header, got: %s", receivedAccountHeader)
	}
}

func TestRefreshAccountProfile_FreeAccountFallsBackToStoredPlanTypeWhenQuotaPayloadOmitsPlanType(t *testing.T) {
	authServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token":  "at-free",
			"refresh_token": "rt-free-rotated",
			"expires_in":    3600,
			"id_token":      testServiceIDTokenWithProfile("free", "acc-free", "user-free", "org-free"),
		})
	}))
	defer authServer.Close()

	quotaServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"rate_limit": map[string]any{
				"primary_window": map[string]any{
					"used_percent":        9,
					"reset_after_seconds": 600,
				},
			},
		})
	}))
	defer quotaServer.Close()

	svc, st, accountID := newOAuthProfileTestService(t, accountauth.BuildStoredOpenAICredential(
		"rt-free",
		"",
		"",
		accountauth.OpenAIAccountProfile{
			PlanType:         "free",
			ChatGPTAccountID: "acc-free",
		},
		time.Time{},
	))

	oldRefreshURL := accountauthOpenAIRefreshURL()
	oldQuotaURL := chatGPTWhamUsageURL
	t.Cleanup(func() {
		setAccountauthOpenAIRefreshURL(oldRefreshURL)
		chatGPTWhamUsageURL = oldQuotaURL
	})
	setAccountauthOpenAIRefreshURL(authServer.URL)
	chatGPTWhamUsageURL = quotaServer.URL

	result, err := svc.RefreshAccountProfile(context.Background(), accountID)
	if err != nil {
		t.Fatalf("RefreshAccountProfile failed: %v", err)
	}
	if !result.Success || result.QuotaStatus != quotaStatusOK {
		t.Fatalf("unexpected refresh result: %+v", result)
	}

	rec, err := st.GetAccount(context.Background(), accountID)
	if err != nil {
		t.Fatalf("GetAccount failed: %v", err)
	}
	if rec.Quota5HUsedPercent != nil || rec.Quota5HResetAt != nil {
		t.Fatalf("free account should not have 5h quota after fallback normalization: %+v", rec)
	}
	if rec.QuotaWeeklyUsedPercent == nil || *rec.QuotaWeeklyUsedPercent != 9 {
		t.Fatalf("weekly quota should be recovered from primary window: %+v", rec.QuotaWeeklyUsedPercent)
	}
	if rec.QuotaWeeklyResetAt == nil {
		t.Fatalf("weekly reset time should be recovered from primary window")
	}
}

func TestRefreshAccountProfile_BodyPriorityAndHeaderFallback(t *testing.T) {
	authServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token":  "at-team",
			"refresh_token": "rt-team",
			"expires_in":    3600,
			"id_token":      testServiceIDTokenWithProfile("team", "acc-team", "user-team", "org-team"),
		})
	}))
	defer authServer.Close()

	var receivedAccountHeader string
	quotaServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedAccountHeader = r.Header.Get("chatgpt-account-id")
		w.Header().Set("x-codex-plan-type", "team")
		w.Header().Set("x-codex-primary-used-percent", "99")
		w.Header().Set("x-codex-primary-reset-after-seconds", "600")
		w.Header().Set("x-codex-secondary-used-percent", "88")
		w.Header().Set("x-codex-secondary-reset-after-seconds", "3600")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"plan_type": "team",
			"rate_limit": map[string]any{
				"primary_window": map[string]any{
					"used_percent": 40,
				},
				"secondary_window": map[string]any{
					"used_percent": 70,
				},
			},
		})
	}))
	defer quotaServer.Close()

	svc, st, accountID := newOAuthProfileTestService(t, accountauth.BuildStoredOpenAICredential(
		"rt-team",
		"",
		"",
		accountauth.OpenAIAccountProfile{
			PlanType:         "team",
			ChatGPTAccountID: "acc-team",
		},
		time.Time{},
	))

	oldRefreshURL := accountauthOpenAIRefreshURL()
	oldQuotaURL := chatGPTWhamUsageURL
	t.Cleanup(func() {
		setAccountauthOpenAIRefreshURL(oldRefreshURL)
		chatGPTWhamUsageURL = oldQuotaURL
	})
	setAccountauthOpenAIRefreshURL(authServer.URL)
	chatGPTWhamUsageURL = quotaServer.URL

	result, err := svc.RefreshAccountProfile(context.Background(), accountID)
	if err != nil {
		t.Fatalf("RefreshAccountProfile failed: %v", err)
	}
	if !result.Success || result.QuotaStatus != quotaStatusOK {
		t.Fatalf("unexpected refresh result: %+v", result)
	}

	rec, err := st.GetAccount(context.Background(), accountID)
	if err != nil {
		t.Fatalf("GetAccount failed: %v", err)
	}
	if rec.Quota5HUsedPercent == nil || *rec.Quota5HUsedPercent != 40 {
		t.Fatalf("body value should win for 5h quota, got %+v", rec.Quota5HUsedPercent)
	}
	if rec.QuotaWeeklyUsedPercent == nil || *rec.QuotaWeeklyUsedPercent != 70 {
		t.Fatalf("body value should win for weekly quota, got %+v", rec.QuotaWeeklyUsedPercent)
	}
	if rec.Quota5HResetAt == nil || rec.QuotaWeeklyResetAt == nil {
		t.Fatalf("header fallback should populate reset_at values: %+v", rec)
	}
	if receivedAccountHeader != "acc-team" {
		t.Fatalf("expected chatgpt-account-id header, got %s", receivedAccountHeader)
	}
}

func TestRefreshAccountProfile_FreeAccountZeroRemainingMarksExhausted(t *testing.T) {
	authServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token":  "at-free-exhausted",
			"refresh_token": "rt-free-exhausted",
			"expires_in":    3600,
			"id_token":      testServiceIDTokenWithProfile("free", "acc-free-exhausted", "user-free-exhausted", "org-free-exhausted"),
		})
	}))
	defer authServer.Close()

	quotaServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"plan_type": "free",
			"rate_limit": map[string]any{
				"primary_window": map[string]any{
					"used_percent":        100.0,
					"reset_after_seconds": 600,
				},
			},
		})
	}))
	defer quotaServer.Close()

	svc, st, accountID := newOAuthProfileTestService(t, accountauth.BuildStoredOpenAICredential(
		"rt-free-exhausted",
		"",
		"",
		accountauth.OpenAIAccountProfile{},
		time.Time{},
	))

	oldRefreshURL := accountauthOpenAIRefreshURL()
	oldQuotaURL := chatGPTWhamUsageURL
	t.Cleanup(func() {
		setAccountauthOpenAIRefreshURL(oldRefreshURL)
		chatGPTWhamUsageURL = oldQuotaURL
	})
	setAccountauthOpenAIRefreshURL(authServer.URL)
	chatGPTWhamUsageURL = quotaServer.URL

	result, err := svc.RefreshAccountProfile(context.Background(), accountID)
	if err != nil {
		t.Fatalf("RefreshAccountProfile failed: %v", err)
	}
	if !result.Success || result.QuotaStatus != quotaStatusExhausted {
		t.Fatalf("unexpected refresh result: %+v", result)
	}

	rec, err := st.GetAccount(context.Background(), accountID)
	if err != nil {
		t.Fatalf("GetAccount failed: %v", err)
	}
	if rec.QuotaStatus != quotaStatusExhausted {
		t.Fatalf("expected exhausted quota status, got %s", rec.QuotaStatus)
	}
	if rec.QuotaWeeklyUsedPercent == nil || *rec.QuotaWeeklyUsedPercent != 100 {
		t.Fatalf("expected weekly used percent 100, got %+v", rec.QuotaWeeklyUsedPercent)
	}
	if rec.QuotaWeeklyResetAt == nil {
		t.Fatalf("expected weekly reset at to be populated")
	}
}

func TestRefreshAccountProfile_TeamWeeklyZeroRemainingMarksExhausted(t *testing.T) {
	authServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token":  "at-team-exhausted",
			"refresh_token": "rt-team-exhausted",
			"expires_in":    3600,
			"id_token":      testServiceIDTokenWithProfile("team", "acc-team-exhausted", "user-team-exhausted", "org-team-exhausted"),
		})
	}))
	defer authServer.Close()

	quotaServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"plan_type": "team",
			"rate_limit": map[string]any{
				"primary_window": map[string]any{
					"used_percent":        25.0,
					"reset_after_seconds": 600,
				},
				"secondary_window": map[string]any{
					"used_percent":        100.0,
					"reset_after_seconds": 3600,
				},
			},
		})
	}))
	defer quotaServer.Close()

	svc, st, accountID := newOAuthProfileTestService(t, accountauth.BuildStoredOpenAICredential(
		"rt-team-exhausted",
		"",
		"",
		accountauth.OpenAIAccountProfile{
			PlanType:         "team",
			ChatGPTAccountID: "acc-team-exhausted",
		},
		time.Time{},
	))

	oldRefreshURL := accountauthOpenAIRefreshURL()
	oldQuotaURL := chatGPTWhamUsageURL
	t.Cleanup(func() {
		setAccountauthOpenAIRefreshURL(oldRefreshURL)
		chatGPTWhamUsageURL = oldQuotaURL
	})
	setAccountauthOpenAIRefreshURL(authServer.URL)
	chatGPTWhamUsageURL = quotaServer.URL

	result, err := svc.RefreshAccountProfile(context.Background(), accountID)
	if err != nil {
		t.Fatalf("RefreshAccountProfile failed: %v", err)
	}
	if !result.Success || result.QuotaStatus != quotaStatusExhausted {
		t.Fatalf("unexpected refresh result: %+v", result)
	}

	rec, err := st.GetAccount(context.Background(), accountID)
	if err != nil {
		t.Fatalf("GetAccount failed: %v", err)
	}
	if rec.QuotaStatus != quotaStatusExhausted {
		t.Fatalf("expected exhausted quota status, got %s", rec.QuotaStatus)
	}
	if rec.Quota5HUsedPercent == nil || *rec.Quota5HUsedPercent != 25 {
		t.Fatalf("expected 5h quota preserved, got %+v", rec.Quota5HUsedPercent)
	}
	if rec.QuotaWeeklyUsedPercent == nil || *rec.QuotaWeeklyUsedPercent != 100 {
		t.Fatalf("expected weekly quota 100, got %+v", rec.QuotaWeeklyUsedPercent)
	}
}

func TestRefreshAccountProfile_402WithResetWithoutUsedPercentNormalizesExhaustedWindow(t *testing.T) {
	authServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token":  "at-free-402",
			"refresh_token": "rt-free-402",
			"expires_in":    3600,
			"id_token":      testServiceIDTokenWithProfile("free", "acc-free-402", "user-free-402", "org-free-402"),
		})
	}))
	defer authServer.Close()

	quotaServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusPaymentRequired)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"plan_type": "free",
			"rate_limit": map[string]any{
				"primary_window": map[string]any{
					"reset_after_seconds": 600,
				},
			},
		})
	}))
	defer quotaServer.Close()

	svc, st, accountID := newOAuthProfileTestService(t, accountauth.BuildStoredOpenAICredential(
		"rt-free-402",
		"",
		"",
		accountauth.OpenAIAccountProfile{},
		time.Time{},
	))

	oldRefreshURL := accountauthOpenAIRefreshURL()
	oldQuotaURL := chatGPTWhamUsageURL
	t.Cleanup(func() {
		setAccountauthOpenAIRefreshURL(oldRefreshURL)
		chatGPTWhamUsageURL = oldQuotaURL
	})
	setAccountauthOpenAIRefreshURL(authServer.URL)
	chatGPTWhamUsageURL = quotaServer.URL

	result, err := svc.RefreshAccountProfile(context.Background(), accountID)
	if err != nil {
		t.Fatalf("RefreshAccountProfile failed: %v", err)
	}
	if !result.Success || result.QuotaStatus != quotaStatusExhausted {
		t.Fatalf("unexpected refresh result: %+v", result)
	}

	rec, err := st.GetAccount(context.Background(), accountID)
	if err != nil {
		t.Fatalf("GetAccount failed: %v", err)
	}
	if rec.QuotaStatus != quotaStatusExhausted {
		t.Fatalf("expected exhausted quota status, got %s", rec.QuotaStatus)
	}
	if rec.QuotaWeeklyUsedPercent == nil || *rec.QuotaWeeklyUsedPercent != 100 {
		t.Fatalf("expected weekly used percent normalized to 100, got %+v", rec.QuotaWeeklyUsedPercent)
	}
	if rec.QuotaWeeklyResetAt == nil || !rec.QuotaWeeklyResetAt.After(time.Now()) {
		t.Fatalf("expected future weekly reset after normalization, got %+v", rec.QuotaWeeklyResetAt)
	}

	candidate := classifySchedulableAccount(rec, time.Now())
	if candidate.skipReason != "quota_exhausted_until_reset" {
		t.Fatalf("expected exhausted account skipped until reset, got %+v", candidate)
	}
}

func TestRefreshAccountProfile_402NonFreeDoesNotPoisonOtherWindow(t *testing.T) {
	authServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token":  "at-team-402",
			"refresh_token": "rt-team-402",
			"expires_in":    3600,
			"id_token":      testServiceIDTokenWithProfile("team", "acc-team-402", "user-team-402", "org-team-402"),
		})
	}))
	defer authServer.Close()

	quotaServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusPaymentRequired)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"plan_type": "team",
			"rate_limit": map[string]any{
				"primary_window": map[string]any{
					"reset_after_seconds": 600,
				},
				"secondary_window": map[string]any{
					"used_percent":        35.0,
					"reset_after_seconds": 3600,
				},
			},
		})
	}))
	defer quotaServer.Close()

	svc, st, accountID := newOAuthProfileTestService(t, accountauth.BuildStoredOpenAICredential(
		"rt-team-402",
		"",
		"",
		accountauth.OpenAIAccountProfile{
			PlanType:         "team",
			ChatGPTAccountID: "acc-team-402",
		},
		time.Time{},
	))

	oldRefreshURL := accountauthOpenAIRefreshURL()
	oldQuotaURL := chatGPTWhamUsageURL
	t.Cleanup(func() {
		setAccountauthOpenAIRefreshURL(oldRefreshURL)
		chatGPTWhamUsageURL = oldQuotaURL
	})
	setAccountauthOpenAIRefreshURL(authServer.URL)
	chatGPTWhamUsageURL = quotaServer.URL

	result, err := svc.RefreshAccountProfile(context.Background(), accountID)
	if err != nil {
		t.Fatalf("RefreshAccountProfile failed: %v", err)
	}
	if !result.Success || result.QuotaStatus != quotaStatusExhausted {
		t.Fatalf("unexpected refresh result: %+v", result)
	}

	rec, err := st.GetAccount(context.Background(), accountID)
	if err != nil {
		t.Fatalf("GetAccount failed: %v", err)
	}
	if rec.Quota5HUsedPercent == nil || *rec.Quota5HUsedPercent != 100 {
		t.Fatalf("expected only 5h window normalized to 100, got %+v", rec.Quota5HUsedPercent)
	}
	if rec.QuotaWeeklyUsedPercent == nil || *rec.QuotaWeeklyUsedPercent != 35 {
		t.Fatalf("expected weekly window preserved, got %+v", rec.QuotaWeeklyUsedPercent)
	}
	if rec.Quota5HResetAt == nil {
		t.Fatalf("expected 5h reset time to be populated")
	}

	after5HReset := cloneUpstreamAccountRecord(rec)
	candidate := classifySchedulableAccount(after5HReset, rec.Quota5HResetAt.Add(time.Minute))
	if candidate.skipReason != "" {
		t.Fatalf("expected account schedulable after 5h reset when weekly window remains available, got %+v", candidate)
	}
}

func TestTestUpstreamAccount_ReachableStillTriggersProfileRefresh(t *testing.T) {
	authServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token":  "at-oauth",
			"refresh_token": "rt-oauth",
			"expires_in":    3600,
			"id_token":      testServiceIDTokenWithProfile("plus", "acc-oauth", "user-oauth", "org-oauth"),
		})
	}))
	defer authServer.Close()

	responsesServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"missing required parameter: model"}`, http.StatusBadRequest)
	}))
	defer responsesServer.Close()

	quotaServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"upstream unavailable"}`, http.StatusInternalServerError)
	}))
	defer quotaServer.Close()

	svc, st, accountID := newOAuthProfileTestService(t, accountauth.BuildStoredOpenAICredential(
		"rt-oauth",
		"",
		"",
		accountauth.OpenAIAccountProfile{
			PlanType:         "plus",
			ChatGPTAccountID: "acc-oauth",
		},
		time.Time{},
	))

	oldRefreshURL := accountauthOpenAIRefreshURL()
	oldQuotaURL := chatGPTWhamUsageURL
	oldTestURL := chatGPTCodexTestURL
	t.Cleanup(func() {
		setAccountauthOpenAIRefreshURL(oldRefreshURL)
		chatGPTWhamUsageURL = oldQuotaURL
		chatGPTCodexTestURL = oldTestURL
	})
	setAccountauthOpenAIRefreshURL(authServer.URL)
	chatGPTWhamUsageURL = quotaServer.URL
	chatGPTCodexTestURL = responsesServer.URL

	if err := svc.TestUpstreamAccount(context.Background(), accountID); err != nil {
		t.Fatalf("TestUpstreamAccount should treat reachable OAuth response as success, got %v", err)
	}

	rec, err := st.GetAccount(context.Background(), accountID)
	if err != nil {
		t.Fatalf("GetAccount failed: %v", err)
	}
	if rec.QuotaStatus != quotaStatusUnavailable {
		t.Fatalf("auto refresh should mark quota unavailable, got %s", rec.QuotaStatus)
	}
	if rec.QuotaRefreshedAt == nil {
		t.Fatalf("auto refresh should write quota_refreshed_at")
	}
}

func newOAuthProfileTestService(t *testing.T, credentialRaw string) (*AccountPoolService, store.AccountPoolStore, int64) {
	t.Helper()

	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite failed: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	schemaPath := filepath.Join("..", "tracking", "schema.sql")
	schemaSQL, err := os.ReadFile(schemaPath)
	if err != nil {
		t.Fatalf("read schema failed: %v", err)
	}
	if _, err := db.Exec(string(schemaSQL)); err != nil {
		t.Fatalf("exec schema failed: %v", err)
	}

	st := store.NewSQLiteAccountPoolStore(db)
	rec, err := st.CreateAccount(context.Background(), &store.UpstreamAccountRecord{
		ProviderType:  accountauth.ProviderChatGPTRefreshToken,
		AccountName:   "oauth-test",
		CredentialRaw: credentialRaw,
		BaseURL:       "https://api.openai.com",
		Priority:      1,
		Enabled:       true,
		State:         "active",
	})
	if err != nil {
		t.Fatalf("create account failed: %v", err)
	}

	svc := NewAccountPoolService(st, nil)
	t.Cleanup(func() { _ = svc.Close() })
	return svc, st, rec.ID
}

func testServiceIDTokenWithProfile(planType, accountID, userID, organizationID string) string {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none","typ":"JWT"}`))
	payload := base64.RawURLEncoding.EncodeToString([]byte(`{"https://api.openai.com/auth":{"chatgpt_account_id":"` + accountID + `","chatgpt_plan_type":"` + planType + `","chatgpt_user_id":"` + userID + `","organization_id":"` + organizationID + `"}}`))
	return header + "." + payload + ".signature"
}

func accountauthOpenAIRefreshURL() string {
	return accountauth.CurrentOpenAIRefreshTokenURLForTest()
}

func setAccountauthOpenAIRefreshURL(value string) {
	accountauth.SetOpenAIRefreshTokenURLForTest(value)
}

// 关闭自动 gzip 协商后，quota 接口主动返回的压缩正文必须由刷新链路自行解码。
func TestRefreshAccountProfile_DecodesGzipQuotaBody(t *testing.T) {
	authServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token":  "at-gzip",
			"refresh_token": "rt-gzip",
			"expires_in":    3600,
			"id_token":      testServiceIDTokenWithProfile("plus", "acc-gzip", "user-gzip", "org-gzip"),
		})
	}))
	defer authServer.Close()

	var receivedAcceptEncoding string
	quotaServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedAcceptEncoding = r.Header.Get("Accept-Encoding")
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Encoding", "gzip")
		writer := gzip.NewWriter(w)
		_ = json.NewEncoder(writer).Encode(map[string]any{
			"plan_type": "plus",
			"rate_limit": map[string]any{
				"primary_window": map[string]any{
					"used_percent":        42.0,
					"reset_after_seconds": 600,
				},
				"secondary_window": map[string]any{
					"used_percent":        7.0,
					"reset_after_seconds": 3600,
				},
			},
		})
		_ = writer.Close()
	}))
	defer quotaServer.Close()

	svc, st, accountID := newOAuthProfileTestService(t, accountauth.BuildStoredOpenAICredential(
		"rt-gzip",
		"",
		"",
		accountauth.OpenAIAccountProfile{},
		time.Time{},
	))

	oldRefreshURL := accountauthOpenAIRefreshURL()
	oldQuotaURL := chatGPTWhamUsageURL
	t.Cleanup(func() {
		setAccountauthOpenAIRefreshURL(oldRefreshURL)
		chatGPTWhamUsageURL = oldQuotaURL
	})
	setAccountauthOpenAIRefreshURL(authServer.URL)
	chatGPTWhamUsageURL = quotaServer.URL

	result, err := svc.RefreshAccountProfile(context.Background(), accountID)
	if err != nil {
		t.Fatalf("RefreshAccountProfile failed: %v", err)
	}
	if !result.Success || result.QuotaStatus != quotaStatusOK {
		t.Fatalf("gzip quota body should be decoded and parsed, got %+v", result)
	}
	if receivedAcceptEncoding != "" {
		t.Fatalf("quota request must not advertise Accept-Encoding, got %q", receivedAcceptEncoding)
	}

	rec, err := st.GetAccount(context.Background(), accountID)
	if err != nil {
		t.Fatalf("GetAccount failed: %v", err)
	}
	if rec.Quota5HUsedPercent == nil || *rec.Quota5HUsedPercent != 42 {
		t.Fatalf("unexpected 5h quota: %+v", rec.Quota5HUsedPercent)
	}
	if rec.QuotaWeeklyUsedPercent == nil || *rec.QuotaWeeklyUsedPercent != 7 {
		t.Fatalf("unexpected weekly quota: %+v", rec.QuotaWeeklyUsedPercent)
	}
}

// 连通性测试只对 ChatGPT OAuth 账号伪装 Codex 首方 UA。
func TestTestUpstreamAccount_OAuthUsesCodexUserAgent(t *testing.T) {
	var receivedUserAgent string
	responsesServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedUserAgent = r.Header.Get("User-Agent")
		http.Error(w, `{"error":"missing required parameter: model"}`, http.StatusBadRequest)
	}))
	defer responsesServer.Close()

	quotaServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"upstream unavailable"}`, http.StatusInternalServerError)
	}))
	defer quotaServer.Close()

	svc, _, accountID := newOAuthProfileTestService(t, accountauth.BuildStoredOpenAICredential(
		"rt-ua",
		"at-ua",
		"",
		accountauth.OpenAIAccountProfile{
			PlanType:         "plus",
			ChatGPTAccountID: "acc-ua",
		},
		time.Now().Add(time.Hour),
	))

	oldQuotaURL := chatGPTWhamUsageURL
	oldTestURL := chatGPTCodexTestURL
	t.Cleanup(func() {
		chatGPTWhamUsageURL = oldQuotaURL
		chatGPTCodexTestURL = oldTestURL
	})
	chatGPTWhamUsageURL = quotaServer.URL
	chatGPTCodexTestURL = responsesServer.URL

	if err := svc.TestUpstreamAccount(context.Background(), accountID); err != nil {
		t.Fatalf("TestUpstreamAccount failed: %v", err)
	}
	if receivedUserAgent != accountauth.CodexBaseUserAgent() {
		t.Fatalf("OAuth connectivity test should send Codex base UA, got %q", receivedUserAgent)
	}
}
