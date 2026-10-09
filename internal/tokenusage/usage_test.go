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
