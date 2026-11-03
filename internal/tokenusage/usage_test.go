package tokenusage

import (
	"database/sql"
	"testing"

	_ "modernc.org/sqlite"
)

func TestTotalTokens_GoAndSQLAgree(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	query := `SELECT ` + TotalTokensSQL + ` FROM (
		SELECT ? AS request_family, ? AS path, ? AS input_tokens, ? AS output_tokens,
			? AS cache_creation_tokens, ? AS cache_read_tokens)`

	for _, tc := range []struct {
		name, family, path string
		want               int64
	}{
		{"Codex", "codex", "/v1/responses", 273748},
		{"订阅入口", "codex", "/backend-api/codex/responses", 273748},
		{"独立压缩", "codex", "/v1/responses/compact", 273748},
		{"历史路径", "", "/v1/responses", 273748},
		{"历史订阅路径", "other", "/backend-api/codex/responses/compact", 273748},
		{"规范化", " OTHER ", " /V1/RESPONSES ", 273748},
		{"明确请求族优先", "codex", "/legacy", 273748},
		{"Claude", "claude", "/v1/messages", 540907},
		{"明确Claude优先", "claude", "/v1/responses", 540907},
		{"其他协议", "other", "/custom", 540907},
		{"路径不能部分命中", "", "/v1/responses-extra", 540907},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := TotalTokens(tc.family, tc.path, 272077, 1671, 2000, 265159)
			if got != tc.want {
				t.Fatalf("Go 总量 = %d，期望 %d", got, tc.want)
			}
			var stored int64
			if err := db.QueryRow(query, tc.family, tc.path, 272077, 1671, 2000, 265159).Scan(&stored); err != nil {
				t.Fatal(err)
			}
			if stored != tc.want {
				t.Fatalf("SQL 总量 = %d，期望 %d", stored, tc.want)
			}
		})
	}
	for _, tc := range []struct {
		name string
		args []any
		want int64
	}{
		{"全部未知", []any{nil, nil, nil, nil, nil, nil}, 0},
		{"历史路径与空分项", []any{nil, "/v1/responses", 100, 5, nil, 80}, 105},
		{"仅输出已知", []any{"codex", "/v1/responses", nil, 5, nil, nil}, 5},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var got int64
			if err := db.QueryRow(query, tc.args...).Scan(&got); err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Fatalf("总量 = %d，期望 %d", got, tc.want)
			}
		})
	}
}

func TestFullInput_KnownZeroNullAndProtocol(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	query := `SELECT ` + FullInputSQL + ` FROM (SELECT ? AS request_family, ? AS path, ? AS input_tokens,
		? AS cache_creation_tokens, 100 AS cache_creation_5m_tokens, 200 AS cache_creation_1h_tokens, ? AS cache_read_tokens)`
	for _, tc := range []struct {
		family, path          string
		input, creation, read interface{}
		want                  sql.NullInt64
	}{
		{"claude", "/v1/messages", 100, 300, 600, sql.NullInt64{Int64: 1000, Valid: true}},
		{"claude", "/v1/messages", 100, nil, 600, sql.NullInt64{Int64: 1000, Valid: true}},
		{"codex", "/v1/responses", 1000, 300, 900, sql.NullInt64{Int64: 1000, Valid: true}},
		{"other", "/backend-api/codex/responses/compact", 1000, 300, 900, sql.NullInt64{Int64: 1000, Valid: true}},
		{"codex", "/v1/responses", 0, nil, nil, sql.NullInt64{Valid: true}},
		{"claude", "/v1/messages", 100, 300, nil, sql.NullInt64{}},
		{"codex", "/v1/responses", nil, 0, 0, sql.NullInt64{}},
	} {
		var got sql.NullInt64
		if err := db.QueryRow(query, tc.family, tc.path, tc.input, tc.creation, tc.read).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != tc.want {
			t.Fatalf("%+v: got %+v", tc, got)
		}
	}
	if TotalTokens("claude", "/v1/messages", 100, 0, 300, 600) != 1000 || TotalTokens("codex", "/v1/responses", 1000, 0, 300, 900) != 1000 {
		t.Fatal("Go 完整输入与 SQL 口径不一致")
	}
}
