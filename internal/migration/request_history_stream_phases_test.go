package migration

import (
	"context"
	"reflect"
	"testing"

	"sinuo/internal/tracking"
)

func TestStreamPhases_HistoryRebuildPreservesOrKeepsNull(t *testing.T) {
	for _, existing := range []bool{true, false} {
		t.Run(map[bool]string{true: "existing", false: "legacy"}[existing], func(t *testing.T) {
			db := openLegacyFixtureDB(t)
			defer db.Close()
			if existing {
				for _, field := range []string{"first_content_ms INTEGER", "first_text_ms INTEGER", "first_reasoning_ms INTEGER", "reasoning_hidden INTEGER", "stream_flow_ms INTEGER", "stop_reason TEXT", "upstream_request_id TEXT"} {
					if _, err := db.Exec("ALTER TABLE request_logs ADD COLUMN " + field); err != nil {
						t.Fatal(err)
					}
				}
				if _, err := db.Exec("UPDATE request_logs SET first_content_ms=0,first_text_ms=20,first_reasoning_ms=0,reasoning_hidden=1,stream_flow_ms=500,stop_reason='completed',upstream_request_id='synthetic-history'"); err != nil {
					t.Fatal(err)
				}
			}
			var previous tracking.StreamPhases
			for i := 0; i < 2; i++ {
				tx, err := db.BeginTx(context.Background(), nil)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := migrateRequestHistory(context.Background(), tx); err != nil {
					_ = tx.Rollback()
					t.Fatal(err)
				}
				if err := tx.Commit(); err != nil {
					t.Fatal(err)
				}
				var p tracking.StreamPhases
				err = db.QueryRow("SELECT first_content_ms,first_text_ms,first_reasoning_ms,reasoning_hidden,stream_flow_ms,stop_reason,upstream_request_id FROM request_logs WHERE request_id=?", "req-fixture-codex").Scan(&p.FirstContentMs, &p.FirstTextMs, &p.FirstReasoningMs, &p.ReasoningHidden, &p.StreamFlowMs, &p.StopReason, &p.UpstreamRequestID)
				if err != nil {
					t.Fatal(err)
				}
				if existing {
					if p.FirstContentMs == nil || *p.FirstContentMs != 0 || *p.FirstTextMs != 20 || *p.FirstReasoningMs != 0 || !*p.ReasoningHidden || *p.StreamFlowMs != 500 || *p.StopReason != "completed" || *p.UpstreamRequestID != "synthetic-history" {
						t.Fatalf("%+v", p)
					}
				} else if !reflect.DeepEqual(p, tracking.StreamPhases{}) {
					t.Fatal("历史被回填")
				}
				if i > 0 && !reflect.DeepEqual(previous, p) {
					t.Fatal("再次重建丢字段")
				}
				previous = p
			}
		})
	}
}
