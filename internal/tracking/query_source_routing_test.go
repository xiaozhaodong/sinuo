package tracking

import (
	"context"
	"testing"
	"time"

	timezonepolicy "sinuo/internal/timezone"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newSourceRoutingTracker(t *testing.T) *UsageTracker {
	t.Helper()

	cfg := &Config{
		Enabled:         true,
		DatabasePath:    ":memory:",
		BufferSize:      32,
		BatchSize:       8,
		FlushInterval:   50 * time.Millisecond,
		MaxRetry:        3,
		CleanupInterval: 24 * time.Hour,
		RetentionDays:   30,
	}

	tracker, err := NewUsageTracker(cfg)
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = tracker.Close()
	})
	return tracker
}

func insertEndpointRequestLog(t *testing.T, tracker *UsageTracker, requestID string, startTime time.Time) {
	t.Helper()
	db := tracker.GetWriteDB()
	require.NotNil(t, db)

	_, err := db.Exec(`
		INSERT INTO request_logs (
			request_id, start_time, status, request_family, upstream_type,
			endpoint_name, upstream_name, model_name
		) VALUES (?, ?, 'completed', 'claude', 'endpoint', ?, ?, ?)`,
		requestID,
		timezonepolicy.FormatStorage(startTime),
		"ep-"+requestID,
		"ep-"+requestID,
		"gpt-4.1",
	)
	require.NoError(t, err)
}

func insertAccountRequestLog(t *testing.T, tracker *UsageTracker, requestID string, startTime time.Time, accountName string) {
	t.Helper()
	db := tracker.GetWriteDB()
	require.NotNil(t, db)

	_, err := db.Exec(`
		INSERT INTO request_logs (
			request_id, start_time, status, request_family, upstream_type,
			endpoint_name,
			upstream_source_name, upstream_name, upstream_id, model_name
		) VALUES (?, ?, 'completed', 'codex', 'account', ?, ?, ?, ?, ?)`,
		requestID,
		timezonepolicy.FormatStorage(startTime),
		accountName,
		"",
		accountName,
		0,
		"gpt-4.1",
	)
	require.NoError(t, err)
}

func requestIDs(details []RequestDetail) map[string]struct{} {
	out := make(map[string]struct{}, len(details))
	for _, d := range details {
		out[d.RequestID] = struct{}{}
	}
	return out
}

func TestQueryRequestDetails_AccountSourceUsesRequestLogs(t *testing.T) {
	tracker := newSourceRoutingTracker(t)
	now := time.Now()

	insertAccountRequestLog(t, tracker, "req-account-table", now.Add(-2*time.Minute), "acc-a")
	insertAccountRequestLog(t, tracker, "req-account-second", now.Add(-1*time.Minute), "acc-b")
	insertEndpointRequestLog(t, tracker, "req-endpoint-table", now.Add(-30*time.Second))

	ctx := context.Background()

	accountDetails, err := tracker.QueryRequestDetails(ctx, &QueryOptions{
		UpstreamType: "account",
		Limit:        20,
	})
	require.NoError(t, err)
	require.Len(t, accountDetails, 2)
	assert.Equal(t, "req-account-second", accountDetails[0].RequestID)
	assert.Equal(t, "account", accountDetails[0].UpstreamType)
	assert.Equal(t, "", accountDetails[0].UpstreamSourceName)
	assert.Equal(t, "acc-b", accountDetails[0].UpstreamName)
	assert.Equal(t, RequestFamilyCodex, accountDetails[0].RequestFamily)

	accountCount, err := tracker.CountRequestDetails(ctx, &QueryOptions{
		UpstreamType: "account",
	})
	require.NoError(t, err)
	assert.Equal(t, 2, accountCount)

	allDetails, err := tracker.QueryRequestDetails(ctx, &QueryOptions{
		UpstreamType: "",
		Limit:        20,
	})
	require.NoError(t, err)
	assert.Len(t, allDetails, 3)
	allIDs := requestIDs(allDetails)
	_, hasEndpoint := allIDs["req-endpoint-table"]
	_, hasAccountA := allIDs["req-account-table"]
	_, hasAccountB := allIDs["req-account-second"]
	assert.True(t, hasEndpoint)
	assert.True(t, hasAccountA)
	assert.True(t, hasAccountB)

	allCount, err := tracker.CountRequestDetails(ctx, &QueryOptions{
		UpstreamType: "",
	})
	require.NoError(t, err)
	assert.Equal(t, 3, allCount)
}

func TestQueryRequestDetailsWithHotPool_AccountSourceDedupAndPagination(t *testing.T) {
	tracker := newSourceRoutingTracker(t)
	now := time.Now()

	insertAccountRequestLog(t, tracker, "req-account-db", now.Add(-5*time.Minute), "acc-db")
	insertAccountRequestLog(t, tracker, "req-account-hot", now.Add(-4*time.Minute), "acc-hot")

	tracker.RecordRequestStart("req-account-hot", "127.0.0.1", "codex-cli", "POST", "/v1/responses", true)
	upstreamType := "account"
	accountName := "acc-hot"
	accountID := int64(9001)
	status := "processing"
	httpStatus := 200
	tracker.RecordRequestUpdate("req-account-hot", UpdateOptions{
		UpstreamType: &upstreamType,
		UpstreamName: &accountName,
		UpstreamID:   &accountID,
		Status:       &status,
		HttpStatus:   &httpStatus,
	})

	ctx := context.Background()
	details, total, err := tracker.QueryRequestDetailsWithHotPool(ctx, &QueryOptions{
		UpstreamType: "account",
		Limit:        10,
		Offset:       0,
	})
	require.NoError(t, err)
	assert.Equal(t, int64(2), total, "hot pool + db overlap should be de-duplicated")
	assert.Len(t, details, 2)
	ids := requestIDs(details)
	_, hasHot := ids["req-account-hot"]
	_, hasDB := ids["req-account-db"]
	assert.True(t, hasHot)
	assert.True(t, hasDB)

	page2, total2, err := tracker.QueryRequestDetailsWithHotPool(ctx, &QueryOptions{
		UpstreamType: "account",
		Limit:        1,
		Offset:       1,
	})
	require.NoError(t, err)
	assert.Equal(t, int64(2), total2)
	require.Len(t, page2, 1)
	assert.Equal(t, "req-account-db", page2[0].RequestID)
}
