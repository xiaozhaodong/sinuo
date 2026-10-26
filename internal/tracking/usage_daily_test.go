package tracking

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	timezonepolicy "sinuo/internal/timezone"
)

// 在真实 modernc 单连接上观测事务/聚合，避免用耗时猜测是否命中。
type dailyTestState struct {
	begins, aggregates, aggregatedDays atomic.Int64
	fail                               string
	afterCacheRead                     func()
	beforeCacheWrite                   func()
}
type dailyTestConnector struct {
	driver.Connector
	state *dailyTestState
}
type dailyTestConn struct {
	sqliteDriverConn
	state *dailyTestState
}
type dailyTestRows struct {
	driver.Rows
	closed func()
}

func (r *dailyTestRows) Close() error {
	err := r.Rows.Close()
	if r.closed != nil {
		r.closed()
	}
	return err
}
func (c *dailyTestConnector) Connect(ctx context.Context) (driver.Conn, error) {
	conn, err := c.Connector.Connect(ctx)
	if err != nil {
		return nil, err
	}
	return &dailyTestConn{conn.(sqliteDriverConn), c.state}, nil
}
func (c *dailyTestConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	c.state.begins.Add(1)
	if c.state.fail == "begin" {
		return nil, errors.New("injected begin failure")
	}
	return c.sqliteDriverConn.BeginTx(ctx, opts)
}
func (c *dailyTestConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	aggregate := strings.HasPrefix(query, "WITH buckets(")
	cache := strings.Contains(query, "FROM usage_daily WHERE timezone_name")
	if aggregate {
		c.state.aggregates.Add(1)
		c.state.aggregatedDays.Add(int64(strings.Count(strings.Split(query, "SELECT b.bucket_index")[0], "(?, ?, ?)")))
	}
	if (aggregate && c.state.fail == "aggregate") || (cache && c.state.fail == "read") {
		return nil, errors.New("injected query failure")
	}
	rows, err := c.sqliteDriverConn.QueryContext(ctx, query, args)
	if err == nil && cache && c.state.afterCacheRead != nil {
		return &dailyTestRows{rows, c.state.afterCacheRead}, nil
	}
	return rows, err
}
func (c *dailyTestConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	if strings.HasPrefix(query, "INSERT INTO usage_daily (") {
		if c.state.beforeCacheWrite != nil {
			c.state.beforeCacheWrite()
		}
		if c.state.fail == "write" {
			return nil, errors.New("injected write failure")
		}
	}
	return c.sqliteDriverConn.ExecContext(ctx, query, args)
}
func newDailyTestTracker(t *testing.T) (*UsageTracker, *dailyTestState, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "usage.db")
	state := &dailyTestState{}
	db := sql.OpenDB(&dailyTestConnector{newSQLiteConnector(path + sqliteDSNParams), state})
	db.SetMaxOpenConns(1)
	adapter := &SQLiteAdapter{db: db}
	schema, err := sqliteSchemaFS.ReadFile("schema.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(string(schema)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return &UsageTracker{readDB: db, writeDB: db, adapter: adapter}, state, path
}
func dailyExec(t *testing.T, db *sql.DB, query string, args ...any) {
	t.Helper()
	if _, err := db.Exec(query, args...); err != nil {
		t.Fatal(err)
	}
}
func dailyCacheCount(t *testing.T, tracker *UsageTracker) int {
	t.Helper()
	var n int
	if err := tracker.readDB.QueryRow("SELECT COUNT(*) FROM usage_daily").Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}
func dailyQuery(t *testing.T, tracker *UsageTracker, policy *timezonepolicy.Policy, buckets []TimeBucket, now time.Time) []DailyUsage {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	got, err := tracker.QueryUsageHeatmapDaily(ctx, policy, buckets, now)
	if err != nil {
		t.Fatal(err)
	}
	return got
}
func assertDailyEqual(t *testing.T, got, want []DailyUsage) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("日数 %d != %d", len(got), len(want))
	}
	for i := range want {
		a, b := got[i], want[i]
		if math.Abs(a.TotalCostUSD-b.TotalCostUSD) > 1e-10 {
			t.Fatalf("日期 %d 成本不等: %+v / %+v", i, a, b)
		}
		a.TotalCostUSD, b.TotalCostUSD = 0, 0
		if !reflect.DeepEqual(a, b) {
			t.Fatalf("日期 %d 不等: %+v / %+v", i, a, b)
		}
	}
}
func assertDailyMatchesDirect(t *testing.T, tracker *UsageTracker, policy *timezonepolicy.Policy, buckets []TimeBucket, now time.Time) {
	t.Helper()
	got := dailyQuery(t, tracker, policy, buckets, now)
	want, err := tracker.QueryDailyUsageWithHotPool(context.Background(), buckets)
	if err != nil {
		t.Fatal(err)
	}
	assertDailyEqual(t, got, want)
}

func TestUsageDailyParityAndDST(t *testing.T) {
	for _, dates := range [][2]string{{"2026-03-07", "2026-03-11"}, {"2026-10-31", "2026-11-04"}} {
		t.Run(dates[0], func(t *testing.T) {
			tracker, _, _ := newDailyTestTracker(t)
			policy := mustPolicy(t, "America/New_York")
			buckets := heatmapTestBuckets(t, policy.Name(), dates[0], dates[1])
			now := buckets[len(buckets)-1].Start.Add(time.Hour)
			for day, bucket := range buckets {
				for i, kind := range []string{RequestKindLegacy, RequestKindGeneration, RequestKindCompaction} {
					for j, family := range []string{"claude", "codex", "other"} {
						for k, path := range []string{"/v1/messages", "/v1/responses", "/backend-api/codex/responses/compact"} {
							id := fmt.Sprintf("%d-%d-%d-%d", day, i, j, k)
							seedHeatmapRequest(t, tracker, id, kind, family, path, bucket.End.Add(-time.Microsecond), 100, 20, 10, 30, .13)
						}
					}
				}
			}
			assertDailyMatchesDirect(t, tracker, policy, buckets, now)
			assertDailyMatchesDirect(t, tracker, policy, buckets, now)
			if dailyCacheCount(t, tracker) != 3 {
				t.Fatal("只封存前天及更早")
			}
		})
	}
}

func TestUsageDailyHitZeroDaysAndRollover(t *testing.T) {
	tracker, state, _ := newDailyTestTracker(t)
	policy := mustPolicy(t, "UTC")
	buckets := heatmapTestBuckets(t, "UTC", "2026-10-20", "2026-10-24")
	now := buckets[4].Start.Add(time.Hour)
	dailyQuery(t, tracker, policy, buckets, now)
	if dailyCacheCount(t, tracker) != 3 {
		t.Fatal("零日也必须缓存")
	}
	begins, queries, days := state.begins.Load(), state.aggregates.Load(), state.aggregatedDays.Load()
	dailyQuery(t, tracker, policy, buckets[:3], now)
	if state.begins.Load() != begins || state.aggregates.Load() != queries {
		t.Fatal("命中缓存不应开启事务或聚合")
	}
	dailyQuery(t, tracker, policy, buckets, now.AddDate(0, 0, 1))
	if dailyCacheCount(t, tracker) != 4 || state.aggregatedDays.Load()-days != 2 {
		t.Fatal("跨日只新封存一天，另一天实时聚合")
	}
	// 只有今天的短区间完全不进入封存事务。
	begins = state.begins.Load()
	dailyQuery(t, tracker, policy, buckets[4:], now)
	if state.begins.Load() != begins {
		t.Fatal("短区间不应封存")
	}
}

func TestUsageDailyInvalidatesEveryStatColumn(t *testing.T) {
	columns := []struct {
		name  string
		value any
	}{
		{"start_time", "2026-10-20T01:00:00.000000Z"}, {"request_kind", "compaction"},
		{"request_family", "codex"}, {"path", "/v1/responses"}, {"input_tokens", 101},
		{"output_tokens", 102}, {"cache_creation_tokens", 103}, {"cache_read_tokens", 104}, {"total_cost_usd", 9.5},
	}
	tracker, _, _ := newDailyTestTracker(t)
	policy := mustPolicy(t, "UTC")
	buckets := heatmapTestBuckets(t, "UTC", "2026-10-20", "2026-10-21")
	now := buckets[1].End.AddDate(0, 0, 2)
	seedHeatmapRequest(t, tracker, "stat", RequestKindGeneration, "claude", "/v1/messages", buckets[0].Start, 10, 2, 3, 4, 1)
	for _, column := range columns {
		t.Run(column.name, func(t *testing.T) {
			dailyQuery(t, tracker, policy, buckets, now)
			dailyExec(t, tracker.writeDB, "UPDATE request_logs SET "+column.name+" = ? WHERE request_id = ?", column.value, "stat")
			if dailyCacheCount(t, tracker) != 1 {
				t.Fatal("统计列更新必须只失效所属日")
			}
			assertDailyMatchesDirect(t, tracker, policy, buckets, now)
		})
	}
	// 通过 SQLite 编译器收集实际 SQL 读取列，新增统计列时测试会要求加入上面的逐列用例。
	query, args, err := dailyUsageQuery(buckets, nil)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := tracker.readDB.Query("EXPLAIN "+query, args...)
	if err != nil {
		t.Fatal(err)
	}
	var rootPage int
	// 取列信息前先关闭 EXPLAIN，遵守单连接约束。
	type instruction struct {
		opcode     string
		p1, p2, p3 int
		p4         any
	}
	var code []instruction
	for rows.Next() {
		var address, p5 int
		var comment any
		var op instruction
		if err := rows.Scan(&address, &op.opcode, &op.p1, &op.p2, &op.p3, &op.p4, &p5, &comment); err != nil {
			t.Fatal(err)
		}
		code = append(code, op)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	rows.Close()
	if err := tracker.readDB.QueryRow("SELECT rootpage FROM sqlite_master WHERE name='request_logs'").Scan(&rootPage); err != nil {
		t.Fatal(err)
	}
	columnRows, err := tracker.readDB.Query("PRAGMA table_info(request_logs)")
	if err != nil {
		t.Fatal(err)
	}
	names := map[int]string{}
	for columnRows.Next() {
		var cid, notnull, pk int
		var name, kind string
		var def any
		if err := columnRows.Scan(&cid, &name, &kind, &notnull, &def, &pk); err != nil {
			t.Fatal(err)
		}
		names[cid] = name
	}
	if err := columnRows.Err(); err != nil {
		t.Fatal(err)
	}
	columnRows.Close()
	cursors, checked := map[int]bool{}, map[string]bool{"start_time": true}
	for _, column := range columns {
		checked[column.name] = true
	}
	for _, op := range code {
		if op.opcode == "OpenRead" && op.p2 == rootPage {
			cursors[op.p1] = true
		}
	}
	for _, op := range code {
		if op.opcode == "Column" && cursors[op.p1] && !checked[names[op.p2]] {
			t.Fatalf("新增统计列 %s 需要触发器失效测试", names[op.p2])
		}
	}
}

func TestUsageDailyInsertUpsertMoveDeleteAndNonStatUpdate(t *testing.T) {
	tracker, _, _ := newDailyTestTracker(t)
	policy := mustPolicy(t, "UTC")
	buckets := heatmapTestBuckets(t, "UTC", "2026-10-20", "2026-10-22")
	now := buckets[2].End.AddDate(0, 0, 2)
	dailyQuery(t, tracker, policy, buckets, now)
	seedHeatmapRequest(t, tracker, "old", RequestKindLegacy, "claude", "", buckets[0].Start, 10, 2, 3, 4, 1)
	if dailyCacheCount(t, tracker) != 2 {
		t.Fatal("旧日 INSERT 未失效")
	}
	assertDailyMatchesDirect(t, tracker, policy, buckets, now)
	query := tracker.adapter.BuildInsertOrReplaceQuery("request_logs", []string{"request_id", "start_time", "input_tokens"}, []string{"?", "?", "?"})
	dailyExec(t, tracker.writeDB, query, "old", timezonepolicy.FormatStorage(buckets[0].Start), 70)
	if dailyCacheCount(t, tracker) != 2 {
		t.Fatal("ON CONFLICT UPDATE 未失效")
	}
	assertDailyMatchesDirect(t, tracker, policy, buckets, now)
	dailyExec(t, tracker.writeDB, "UPDATE request_logs SET completion_ms=123 WHERE request_id=?", "old")
	if dailyCacheCount(t, tracker) != 3 {
		t.Fatal("非统计列不应失效")
	}
	dailyExec(t, tracker.writeDB, "UPDATE request_logs SET start_time=? WHERE request_id=?", timezonepolicy.FormatStorage(buckets[1].Start), "old")
	if dailyCacheCount(t, tracker) != 1 {
		t.Fatal("跨日必须同时失效新旧日")
	}
	assertDailyMatchesDirect(t, tracker, policy, buckets, now)
	dailyExec(t, tracker.writeDB, "DELETE FROM request_logs WHERE request_id=?", "old")
	if dailyCacheCount(t, tracker) != 2 {
		t.Fatal("DELETE 未失效")
	}
	assertDailyMatchesDirect(t, tracker, policy, buckets, now)
}
