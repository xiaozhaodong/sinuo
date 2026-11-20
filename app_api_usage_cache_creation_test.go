package main

import (
	"encoding/json"
	"testing"
	"time"

	timezonepolicy "sinuo/internal/timezone"
)

func TestRequestDetails_PreserveNullableCacheCreation(t *testing.T) {
	app := newUsageStatsTestApp(t)
	for _, tc := range []struct {
		id                 string
		total, short, long any
	}{
		{"missing", nil, nil, nil},
		{"zero", 0, nil, nil},
		{"partial", nil, 20, nil},
		{"split", nil, 20, 5},
	} {
		t.Run(tc.id, func(t *testing.T) {
			_, err := app.usageTracker.GetWriteDB().Exec(`INSERT INTO request_logs
				(request_id, start_time, status, request_family, input_tokens, output_tokens,
				 cache_creation_tokens, cache_creation_5m_tokens, cache_creation_1h_tokens, cache_read_tokens)
				VALUES (?, ?, 'completed', 'claude', 2840, 412, ?, ?, ?, 960)`,
				tc.id, timezonepolicy.FormatStorage(time.Now()), tc.total, tc.short, tc.long)
			if err != nil {
				t.Fatal(err)
			}
			detail, err := app.GetRequestLifecycleDetail(tc.id)
			if err != nil || detail.Request == nil {
				t.Fatalf("读取请求详情失败: %v", err)
			}
			raw, err := json.Marshal(detail.Request)
			if err != nil {
				t.Fatal(err)
			}
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(raw, &fields); err != nil {
				t.Fatal(err)
			}
			for key, want := range map[string]any{
				"cache_creation_tokens": tc.total, "cache_creation_5m_tokens": tc.short, "cache_creation_1h_tokens": tc.long,
			} {
				expected, err := json.Marshal(want)
				if err != nil {
					t.Fatal(err)
				}
				if string(fields[key]) != string(expected) {
					t.Errorf("%s = %s，期望 %s", key, fields[key], expected)
				}
			}
		})
	}
}
