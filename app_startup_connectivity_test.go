package main

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"sinuo/config"
	"sinuo/internal/endpoint"
	"sinuo/internal/store"
)

type syncWriter struct {
	mu  sync.Mutex
	buf []byte
}

func (w *syncWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.buf = append(w.buf, p...)
	return len(p), nil
}

func (w *syncWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return string(w.buf)
}

var _ io.Writer = (*syncWriter)(nil)

func TestScheduleStartupConnectivityChecks_NilAppDoesNotPanic(t *testing.T) {
	var app *App
	app.scheduleStartupConnectivityChecks()
}

func TestScheduleStartupConnectivityChecks_DoesNotBlockStartup(t *testing.T) {
	app := NewApp()
	app.endpointManager = endpoint.NewManager(&config.Config{
		Endpoints: []config.EndpointConfig{
			{
				Name: "startup-endpoint",
				URL:  "https://example.com",
			},
		},
	})

	started := make(chan struct{}, 1)
	release := make(chan struct{})
	app.startupEndpointCheckRunner = func() {
		started <- struct{}{}
		<-release
	}

	begin := time.Now()
	app.scheduleStartupConnectivityChecks()
	elapsed := time.Since(begin)

	if elapsed > 50*time.Millisecond {
		t.Fatalf("expected startup checks to be async, took %v", elapsed)
	}

	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("expected endpoint startup check runner to be scheduled")
	}

	close(release)
}

func TestScheduleStartupConnectivityChecks_DoesNotBlockStartupWhenAccountChecksScheduled(t *testing.T) {
	app := NewApp()

	started := make(chan struct{}, 1)
	release := make(chan struct{})
	app.startupAccountCheckRunner = func() {
		started <- struct{}{}
		<-release
	}

	begin := time.Now()
	app.scheduleStartupConnectivityChecks()
	elapsed := time.Since(begin)

	if elapsed > 50*time.Millisecond {
		t.Fatalf("expected startup account checks to be async, took %v", elapsed)
	}

	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("expected account startup check runner to be scheduled")
	}

	close(release)
}

func TestScheduleStartupConnectivityChecks_SkipsWhenNothingConfigured(t *testing.T) {
	app := NewApp()

	if app.shouldRunStartupEndpointChecks() {
		t.Fatal("expected endpoint startup checks to be skipped when no endpoints are configured")
	}
	if app.shouldRunStartupAccountChecks() {
		t.Fatal("expected account startup checks to be skipped when no account service is configured")
	}

	app.scheduleStartupConnectivityChecks()
}

func TestScheduleStartupConnectivityChecks_TriggersAccountChecksWhenEligibleAccountsExist(t *testing.T) {
	app, _ := newAccountPoolAPITestApp(t)
	app.ctx = context.Background()

	requests := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/responses" {
			t.Fatalf("expected request to /v1/responses, got %s", r.URL.Path)
		}
		requests <- struct{}{}
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer server.Close()

	_, err := app.accountPoolService.CreateAccount(context.Background(), &store.UpstreamAccountRecord{
		ProviderType:  "api_key",
		AccountName:   "startup-account",
		CredentialRaw: "sk-startup-account",
		BaseURL:       server.URL,
		Priority:      10,
		Enabled:       true,
		State:         "active",
		QuotaStatus:   "ok",
	})
	if err != nil {
		t.Fatalf("CreateAccount failed: %v", err)
	}

	app.scheduleStartupConnectivityChecks()

	select {
	case <-requests:
	case <-time.After(time.Second):
		t.Fatal("expected startup account checks to trigger upstream connectivity test")
	}
}

func TestScheduleStartupConnectivityChecks_DoesNotTriggerAccountRequestsWhenNoEligibleAccountsExist(t *testing.T) {
	app, _ := newAccountPoolAPITestApp(t)
	app.ctx = context.Background()

	requests := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests <- struct{}{}
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer server.Close()

	logs := &syncWriter{}
	app.logger = slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))

	_, err := app.accountPoolService.CreateAccount(context.Background(), &store.UpstreamAccountRecord{
		ProviderType:  "api_key",
		AccountName:   "disabled-startup-account",
		CredentialRaw: "sk-disabled-startup-account",
		BaseURL:       server.URL,
		Priority:      10,
		Enabled:       false,
		State:         "inactive",
		QuotaStatus:   "ok",
	})
	if err != nil {
		t.Fatalf("CreateAccount disabled failed: %v", err)
	}

	app.scheduleStartupConnectivityChecks()

	deadline := time.Now().Add(time.Second)
	for {
		logText := logs.String()
		if strings.Contains(logText, "账号批量检测完成") || strings.Contains(logText, "无可测账号，跳过账号批量检测") {
			if strings.Contains(logText, "账号批量检测完成") {
				t.Fatalf("expected no completion log when there are no eligible accounts, got logs: %s", logText)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("expected startup account checks to emit a skip signal, got logs: %s", logText)
		}
		time.Sleep(10 * time.Millisecond)
	}

	select {
	case <-requests:
		t.Fatal("expected no upstream connectivity request when no eligible accounts exist")
	default:
	}
}

func TestRunStartupEndpointChecks_DoesNotLogWrapperCompletionOnSuccess(t *testing.T) {
	logs := &syncWriter{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer server.Close()

	app := NewApp()
	app.logger = slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	app.endpointManager = endpoint.NewManager(&config.Config{
		Health: config.HealthConfig{
			Timeout: time.Second,
		},
		Endpoints: []config.EndpointConfig{
			{
				Name:  "startup-endpoint",
				URL:   server.URL,
				Token: "test-token",
			},
		},
	})

	app.runStartupEndpointChecks()

	if strings.Contains(logs.String(), "启动连通性检查: 端点批量检测完成") {
		t.Fatalf("expected no wrapper completion log on endpoint success, got %s", logs.String())
	}
}
