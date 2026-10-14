package endpoint

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"cc-forwarder/config"
)

func TestHealthCheckWithAPIEndpoint(t *testing.T) {
	testCases := []struct {
		name          string
		statusCode    int
		expectHealthy bool
	}{
		{"Success 200", 200, true},
		{"Success 201", 201, true},
		{"Bad Request 400", 400, true},
		{"Unauthorized 401", 401, true},
		{"Forbidden 403", 403, true},
		{"Not Found 404", 404, true},
		{"Server Error 500", 500, false}, // API has issues
		{"Bad Gateway 502", 502, false},  // API unreachable
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Create a test server that returns the specified status code
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				// Add small delay to ensure response time is measurable
				time.Sleep(1 * time.Millisecond)

				// Verify it's checking the correct path
				if r.URL.Path != "/" {
					t.Errorf("Expected request to endpoint URL itself, got path %s", r.URL.Path)
				}
				// Verify Authorization header is present
				if r.Header.Get("Authorization") != "Bearer test-token" {
					t.Errorf("Expected Authorization header 'Bearer test-token', got '%s'", r.Header.Get("Authorization"))
				}
				w.WriteHeader(tc.statusCode)
			}))
			defer server.Close()

			// Create config with test server URL
			cfg := &config.Config{
				Health: config.HealthConfig{
					Timeout: 5 * time.Second,
				},
				Endpoints: []config.EndpointConfig{
					{
						Name:    "test-endpoint",
						URL:     server.URL,
						Token:   "test-token",
						Timeout: 30 * time.Second,
					},
				},
			}

			// Create manager and perform single health check
			manager := NewManager(cfg)
			endpoint := manager.GetAllEndpoints()[0]

			manager.checkEndpointHealth(endpoint)

			// Check result
			if endpoint.IsHealthy() != tc.expectHealthy {
				t.Errorf("Expected healthy=%v for status %d, got %v",
					tc.expectHealthy, tc.statusCode, endpoint.IsHealthy())
			}

			// Verify response time is recorded (should be > 0 for all HTTP responses)
			responseTime := endpoint.GetResponseTime()
			if responseTime <= 0 {
				t.Errorf("Expected response time to be recorded for status %d, got %v", tc.statusCode, responseTime)
			}
		})
	}
}

func TestManagerStartDoesNotPerformAutomaticConnectivityChecks(t *testing.T) {
	requests := make(chan struct{}, 8)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests <- struct{}{}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	cfg := &config.Config{
		Health: config.HealthConfig{
			Timeout: 1 * time.Second,
		},
		Endpoints: []config.EndpointConfig{
			{
				Name:    "auto-check-endpoint",
				URL:     server.URL,
				Token:   "test-token",
				Timeout: 30 * time.Second,
			},
		},
	}

	manager := NewManager(cfg)
	manager.Start()
	defer manager.Stop()

	time.Sleep(80 * time.Millisecond)

	select {
	case <-requests:
		t.Fatal("expected manager start to avoid automatic connectivity checks")
	default:
	}
}

func TestAddEndpointDoesNotTriggerAutomaticConnectivityCheck(t *testing.T) {
	requests := make(chan struct{}, 8)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests <- struct{}{}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	manager := NewManager(&config.Config{})
	err := manager.AddEndpoint(config.EndpointConfig{
		Name: "new-endpoint",
		URL:  server.URL,
	})
	if err != nil {
		t.Fatalf("AddEndpoint returned error: %v", err)
	}

	time.Sleep(50 * time.Millisecond)

	select {
	case <-requests:
		t.Fatal("expected AddEndpoint to avoid automatic connectivity checks")
	default:
	}
}

func TestUpdateEndpointConfigDoesNotTriggerAutomaticConnectivityCheck(t *testing.T) {
	requests := make(chan struct{}, 8)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests <- struct{}{}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	manager := NewManager(&config.Config{
		Endpoints: []config.EndpointConfig{
			{
				Name: "existing-endpoint",
				URL:  "https://old.example.com",
			},
		},
	})

	err := manager.UpdateEndpointConfig("existing-endpoint", config.EndpointConfig{
		URL: server.URL,
	})
	if err != nil {
		t.Fatalf("UpdateEndpointConfig returned error: %v", err)
	}

	time.Sleep(50 * time.Millisecond)

	select {
	case <-requests:
		t.Fatal("expected UpdateEndpointConfig to avoid automatic connectivity checks")
	default:
	}
}

func TestManagerStartCleansExpiredFailureTrackerEntries(t *testing.T) {
	originalCleanupInterval := failureTrackerCleanupInterval
	failureTrackerCleanupInterval = 10 * time.Millisecond
	defer func() {
		failureTrackerCleanupInterval = originalCleanupInterval
	}()

	cfg := &config.Config{
		Health: config.HealthConfig{},
		FailureTracker: config.FailureTrackerConfig{
			Enabled:    true,
			TimeWindow: 10 * time.Millisecond,
			Threshold:  3,
		},
		Endpoints: []config.EndpointConfig{
			{
				Name: "tracked-endpoint",
				URL:  "https://example.com",
			},
		},
	}

	manager := NewManager(cfg)
	manager.RecordSoftFailure("tracked-endpoint", SoftFailureScopeMessages, SoftFailureCategoryConnection)
	trackedEndpointCount := func() int {
		manager.softFailures.mu.Lock()
		defer manager.softFailures.mu.Unlock()
		return len(manager.softFailures.events)
	}

	if got := trackedEndpointCount(); got != 1 {
		t.Fatalf("expected failure tracker to contain one endpoint entry, got %d", got)
	}

	time.Sleep(15 * time.Millisecond)

	manager.Start()
	defer manager.Stop()

	time.Sleep(50 * time.Millisecond)

	if got := trackedEndpointCount(); got != 0 {
		t.Fatalf("expected expired failure tracker entries to be cleaned, got %d", got)
	}
}
