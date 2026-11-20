package tracking

import (
	"context"
	"database/sql"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	timezonepolicy "sinuo/internal/timezone"
)

func TestQueryRequestDetailsWithHotPool_LongRunningRequestDoesNotJumpAcrossArchive(t *testing.T) {
	tr := newModelUsageTestTracker(t)
	ctx := context.Background()
	base := time.Date(2026, 11, 20, 1, 40, 0, 0, time.UTC)

	// 长请求 A 最先发起，期间短请求 B/C/D 已完成并落库。
	seedSessionRequest(t, tr, "short-1", "codex", "sess", base.Add(1*time.Second), 100, 0, 50, 0.1)
	seedSessionRequest(t, tr, "short-2", "codex", "sess", base.Add(2*time.Second), 100, 0, 50, 0.1)
	seedSessionRequest(t, tr, "short-3", "codex", "sess", base.Add(3*time.Second), 100, 0, 50, 0.1)

	longReq := &ActiveRequest{
		RequestID:     "long-hot",
		SessionID:     "sess",
		RequestFamily: "codex",
		StartTime:     base,
		RequestSource: RequestSourceLegacyProxy,
		RequestKind:   RequestKindGeneration,
		UpstreamType:  "account",
		UpstreamName:  "synthetic-upstream",
		ModelName:     "synthetic-model",
		Status:        "processing",
	}
	if err := tr.hotPool.Add(longReq); err != nil {
		t.Fatal(err)
	}

	wantOrder := []string{"short-3", "short-2", "short-1", "long-hot"}
	assertOrder := func(stage string) {
		t.Helper()
		rows, total, err := tr.QueryRequestDetailsWithHotPool(ctx, &QueryOptions{Limit: 20})
		if err != nil {
			t.Fatalf("%s: 查询失败: %v", stage, err)
		}
		if total != int64(len(wantOrder)) {
			t.Fatalf("%s: total = %d, want %d", stage, total, len(wantOrder))
		}
		got := make([]string, len(rows))
		for i, row := range rows {
			got[i] = row.RequestID
		}
		if !reflect.DeepEqual(got, wantOrder) {
			t.Fatalf("%s: 顺序 = %v, want %v", stage, got, wantOrder)
		}
	}

	// 1. 仍在热池 requests 中（进行中）
	assertOrder("热池活跃态")

	// 2. 完成并进入 archiving 缓存（等待定时刷盘）
	tr.hotPool.SetArchiveCallback(func(*ActiveRequest) {})
	if err := tr.hotPool.CompleteAndArchive("long-hot", func(req *ActiveRequest) {
		req.Status = "completed"
	}); err != nil {
		t.Fatal(err)
	}
	assertOrder("热池归档中")

	// 3. 落库并从热池移除后，位置保持完全不变
	seedSessionRequest(t, tr, "long-hot", "codex", "sess", base, 100, 0, 50, 0.1)
	tr.hotPool.ConfirmArchived([]string{"long-hot"})
	assertOrder("归档落库后")
}

func TestQueryRequestDetailsWithHotPool_UnifiedMergePaginationAndLocation(t *testing.T) {
	tr := newModelUsageTestTracker(t)
	ctx := context.Background()
	base := time.Date(2026, 11, 20, 2, 0, 0, 0, time.UTC)

	// DB 中写入若干分布在不同时刻的请求，包括同微秒请求和会被热池覆盖/隐藏的重叠请求。
	seedSessionRequest(t, tr, "db-newest", "codex", "s", base.Add(40*time.Second), 100, 0, 50, 0.1)
	seedSessionRequest(t, tr, "db-mid-2", "codex", "s", base.Add(25*time.Second), 100, 0, 50, 0.1)
	seedSessionRequest(t, tr, "db-tie-a", "codex", "s", base.Add(20*time.Second), 100, 0, 50, 0.1)
	seedSessionRequest(t, tr, "db-tie-c", "codex", "s", base.Add(20*time.Second), 100, 0, 50, 0.1)
	seedSessionRequest(t, tr, "db-mid-1", "codex", "s", base.Add(15*time.Second), 100, 0, 50, 0.1)
	seedSessionRequest(t, tr, "db-old-1", "codex", "s", base.Add(5*time.Second), 100, 0, 50, 0.1)
	// 与热池同 ID 重叠：DB 中匹配 codex，但热池新状态改为 claude，必须从 codex 列表中隐藏。
	seedSessionRequest(t, tr, "overlap-hidden", "codex", "s", base.Add(35*time.Second), 100, 0, 50, 0.1)
	// 与热池同 ID 重叠：热池覆盖了它的最新开始/状态快照。
	seedSessionRequest(t, tr, "overlap-visible", "codex", "s", base.Add(12*time.Second), 100, 0, 50, 0.1)

	hotEntries := []*ActiveRequest{
		{
			RequestID:     "hot-newest",
			SessionID:     "s",
			RequestFamily: "codex",
			StartTime:     base.Add(50 * time.Second),
			RequestSource: RequestSourceLegacyProxy,
			RequestKind:   RequestKindGeneration,
			UpstreamType:  "account",
			Status:        "processing",
		},
		{
			// 与 db-tie-a / db-tie-c 在同一微秒，但带 789ns 尾数：
			// 必须截断到微秒后按 request_id DESC 排在 db-tie-c 与 db-tie-a 之间。
			RequestID:     "db-tie-b",
			SessionID:     "s",
			RequestFamily: "codex",
			StartTime:     base.Add(20*time.Second + 789*time.Nanosecond),
			RequestSource: RequestSourceLegacyProxy,
			RequestKind:   RequestKindGeneration,
			UpstreamType:  "account",
			Status:        "processing",
		},
		{
			RequestID:     "hot-mid",
			SessionID:     "s",
			RequestFamily: "codex",
			StartTime:     base.Add(30 * time.Second),
			RequestSource: RequestSourceLegacyProxy,
			RequestKind:   RequestKindGeneration,
			UpstreamType:  "account",
			Status:        "forwarding",
		},
		{
			RequestID:     "overlap-visible",
			SessionID:     "s",
			RequestFamily: "codex",
			StartTime:     base.Add(12 * time.Second),
			RequestSource: RequestSourceLegacyProxy,
			RequestKind:   RequestKindGeneration,
			UpstreamType:  "account",
			Status:        "processing",
		},
		{
			RequestID:     "hot-oldest",
			SessionID:     "s",
			RequestFamily: "codex",
			StartTime:     base.Add(-10 * time.Second),
			RequestSource: RequestSourceLegacyProxy,
			RequestKind:   RequestKindGeneration,
			UpstreamType:  "account",
			Status:        "pending",
		},
		{
			RequestID:     "overlap-hidden",
			SessionID:     "s",
			RequestFamily: "claude",
			StartTime:     base.Add(35 * time.Second),
			RequestSource: RequestSourceLegacyProxy,
			RequestKind:   RequestKindGeneration,
			UpstreamType:  "endpoint",
			Status:        "processing",
		},
	}
	for _, req := range hotEntries {
		if err := tr.hotPool.Add(req); err != nil {
			t.Fatal(err)
		}
	}

	wantAll := []string{
		"hot-newest",      // +50s
		"db-newest",       // +40s
		"hot-mid",         // +30s
		"db-mid-2",        // +25s
		"db-tie-c",        // +20s, id "db-tie-c"
		"db-tie-b",        // +20s+789ns -> 微秒截断后与 +20s 相等，按 ID 排在 c 与 a 之间
		"db-tie-a",        // +20s, id "db-tie-a"
		"db-mid-1",        // +15s
		"overlap-visible", // +12s（热池版本）
		"db-old-1",        // +5s
		"hot-oldest",      // -10s
	}

	for _, pageSize := range []int{1, 2, 3, 4, 5, 7, 20} {
		var collected []string
		for offset := 0; offset < len(wantAll)+pageSize; offset += pageSize {
			rows, total, err := tr.QueryRequestDetailsWithHotPool(ctx, &QueryOptions{
				RequestFamily: "codex",
				Limit:         pageSize,
				Offset:        offset,
			})
			if err != nil {
				t.Fatalf("pageSize=%d offset=%d: %v", pageSize, offset, err)
			}
			if total != int64(len(wantAll)) {
				t.Fatalf("pageSize=%d offset=%d: total=%d want=%d", pageSize, offset, total, len(wantAll))
			}
			if offset >= len(wantAll) {
				if len(rows) != 0 {
					t.Fatalf("pageSize=%d offset=%d: 越界页应返回空切片, got=%d", pageSize, offset, len(rows))
				}
				continue
			}
			for _, row := range rows {
				collected = append(collected, row.RequestID)
			}
		}
		if !reflect.DeepEqual(collected, wantAll) {
			t.Fatalf("pageSize=%d 逐页拼接顺序不一致:\ngot:  %v\nwant: %v", pageSize, collected, wantAll)
		}

		// 验证每条请求的 LocateRequestPage 页码与实际位置完全一致
		for idx, id := range wantAll {
			wantPage := idx/pageSize + 1
			gotPage, err := tr.LocateRequestPage(ctx, &QueryOptions{RequestFamily: "codex"}, id, pageSize)
			if err != nil {
				t.Fatalf("pageSize=%d locate %s: %v", pageSize, id, err)
			}
			if gotPage != wantPage {
				t.Fatalf("pageSize=%d locate %s: page=%d want=%d", pageSize, id, gotPage, wantPage)
			}
		}
	}
}

func TestQueryRequestDetailsWithHotPool_DeepPageMultiChunkAndEmptySideFastPaths(t *testing.T) {
	tr := newModelUsageTestTracker(t)
	ctx := context.Background()
	base := time.Date(2026, 11, 20, 3, 0, 0, 0, time.UTC)

	// 仅热池有记录时的快速路径
	if err := tr.hotPool.Add(&ActiveRequest{
		RequestID:     "hot-only-1",
		RequestFamily: "claude",
		StartTime:     base.Add(2 * time.Second),
		RequestSource: RequestSourceLegacyProxy,
		RequestKind:   RequestKindGeneration,
		UpstreamType:  "endpoint",
		Status:        "processing",
	}); err != nil {
		t.Fatal(err)
	}
	if err := tr.hotPool.Add(&ActiveRequest{
		RequestID:     "hot-only-2",
		RequestFamily: "claude",
		StartTime:     base.Add(1 * time.Second),
		RequestSource: RequestSourceLegacyProxy,
		RequestKind:   RequestKindGeneration,
		UpstreamType:  "endpoint",
		Status:        "processing",
	}); err != nil {
		t.Fatal(err)
	}
	hotOnlyRows, hotOnlyTotal, err := tr.QueryRequestDetailsWithHotPool(ctx, &QueryOptions{
		RequestFamily: "claude",
		Limit:         1,
		Offset:        1,
	})
	if err != nil || hotOnlyTotal != 2 || len(hotOnlyRows) != 1 || hotOnlyRows[0].RequestID != "hot-only-2" {
		t.Fatalf("仅热池快速路径错误: rows=%+v total=%d err=%v", hotOnlyRows, hotOnlyTotal, err)
	}

	// 构造超过单块上限（256）的 DB 数据 + 跨两端及中间穿插的热池数据，覆盖深页锚点与多块续拉。
	const dbTotal = 310
	for i := 0; i < dbTotal; i++ {
		seedSessionRequest(t, tr, fmt.Sprintf("db-%03d", i), "codex", "bulk", base.Add(time.Duration(i)*time.Second), 100, 0, 50, 0.1)
	}

	// 添加 270 条热池数据：一部分极新，一部分极旧（让锚点落入中间并触发多块续拉）
	// 10 条比所有 DB 更新，260 条比所有 DB 更旧
	var want []string
	for i := 9; i >= 0; i-- {
		id := fmt.Sprintf("hot-new-%03d", i)
		if err := tr.hotPool.Add(&ActiveRequest{
			RequestID:     id,
			SessionID:     "bulk",
			RequestFamily: "codex",
			StartTime:     base.Add(time.Duration(dbTotal+i) * time.Second),
			RequestSource: RequestSourceLegacyProxy,
			RequestKind:   RequestKindGeneration,
			UpstreamType:  "account",
			Status:        "processing",
		}); err != nil {
			t.Fatal(err)
		}
		want = append(want, id)
	}
	for i := dbTotal - 1; i >= 0; i-- {
		want = append(want, fmt.Sprintf("db-%03d", i))
	}
	for i := 259; i >= 0; i-- {
		id := fmt.Sprintf("hot-old-%03d", i)
		if err := tr.hotPool.Add(&ActiveRequest{
			RequestID:     id,
			SessionID:     "bulk",
			RequestFamily: "codex",
			StartTime:     base.Add(-time.Duration(260-i) * time.Second),
			RequestSource: RequestSourceLegacyProxy,
			RequestKind:   RequestKindGeneration,
			UpstreamType:  "account",
			Status:        "processing",
		}); err != nil {
			t.Fatal(err)
		}
		want = append(want, id)
	}

	// 抽查跨越各种锚点与续拉边界的偏移量
	for _, offset := range []int{0, 5, 10, 269, 270, 275, 315, 320, 550, len(want) - 3, len(want)} {
		limit := 15
		rows, total, err := tr.QueryRequestDetailsWithHotPool(ctx, &QueryOptions{
			RequestFamily: "codex",
			Limit:         limit,
			Offset:        offset,
		})
		if err != nil {
			t.Fatalf("offset=%d 查询失败: %v", offset, err)
		}
		if total != int64(len(want)) {
			t.Fatalf("offset=%d total=%d want=%d", offset, total, len(want))
		}
		end := offset + limit
		if end > len(want) {
			end = len(want)
		}
		wantSlice := want[offset:end]
		gotSlice := make([]string, len(rows))
		for i, r := range rows {
			gotSlice[i] = r.RequestID
		}
		if !reflect.DeepEqual(gotSlice, wantSlice) {
			t.Fatalf("offset=%d 结果不一致:\ngot:  %v\nwant: %v", offset, gotSlice, wantSlice)
		}
	}
}

func TestBuildRequestDetailsQuery_ExcludedIDsUsesStartTimeIndex(t *testing.T) {
	tr := newModelUsageTestTracker(t)
	ctx := context.Background()
	now := time.Now()
	opts := modelUsageQueryRange(now)
	opts.Limit = 20
	opts.Offset = 40

	query, args, err := buildRequestDetailsQuery(opts, requestQuerySourceAll, []string{"hot-1", "hot-2"})
	if err != nil {
		t.Fatal(err)
	}
	rows, err := tr.readDB.QueryContext(ctx, "EXPLAIN QUERY PLAN "+query, args...)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()

	var plan strings.Builder
	for rows.Next() {
		var id, parent, notused int
		var detail string
		if err := rows.Scan(&id, &parent, &notused, &detail); err != nil {
			t.Fatal(err)
		}
		plan.WriteString(detail + "\n")
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(plan.String(), "idx_request_logs_start_time") {
		t.Fatalf("带排除热池 ID 的请求查询应继续使用 start_time 索引，实际计划:\n%s", plan.String())
	}
}

func TestQueryRequestDetailsWithHotPool_MultiChunkSnapshotConsistentUnderConcurrentInsert(t *testing.T) {
	tr := newModelUsageTestTracker(t)
	ctx := context.Background()
	base := time.Date(2026, 11, 20, 4, 0, 0, 0, time.UTC)

	// 写入 320 条 DB 记录（db-000 .. db-319）与 245 条更旧的热池记录，
	// 读取第 13 页（offset=240, limit=20）时 initialNeed=260 > 256，必然触发两批 DB 读取：
	// 第 1 批取 db-319..db-064（256 条），第 2 批取 db-063..db-060（4 条）。
	const dbTotal = 320
	for i := 0; i < dbTotal; i++ {
		seedSessionRequest(t, tr, fmt.Sprintf("db-%03d", i), "codex", "snap", base.Add(time.Duration(i)*time.Second), 100, 0, 50, 0.1)
	}
	for i := 0; i < 245; i++ {
		if err := tr.hotPool.Add(&ActiveRequest{
			RequestID:     fmt.Sprintf("hot-old-%03d", i),
			SessionID:     "snap",
			RequestFamily: "codex",
			StartTime:     base.Add(-time.Duration(i+1) * time.Second),
			RequestSource: RequestSourceLegacyProxy,
			RequestKind:   RequestKindGeneration,
			UpstreamType:  "account",
			Status:        "processing",
		}); err != nil {
			t.Fatal(err)
		}
	}

	// 使用独立 SQLite 连接模拟 WAL 模式下的并发落库，避免与单连接 readDB/writeDB 池自锁，
	// 同时真正检验只读事务快照对已提交并发写入的隔离性。
	extWriter := sql.OpenDB(newSQLiteConnector(tr.config.DatabasePath + sqliteDSNParams))
	t.Cleanup(func() { extWriter.Close() })

	batches := 0
	injected := false
	tr.onMergeDBBatchLoaded = func(batchIndex int) {
		batches++
		if batchIndex == 0 && !injected {
			injected = true
			// 在两批 DB 查询之间通过独立 WAL 写连接提交一条更新的请求；
			// 若未共享只读事务，第 2 批按旧 OFFSET 会重复读到 db-064 并挤掉 db-060。
			_, err := extWriter.Exec(`INSERT INTO request_logs
				(request_id, request_family, session_id, start_time, status, request_kind, upstream_type, upstream_name, model_name,
				input_tokens, cache_creation_tokens, cache_creation_5m_tokens, cache_creation_1h_tokens, cache_read_tokens, total_cost_usd)
				VALUES (?, 'codex', 'snap', ?, 'completed', 'generation', 'account', 'synthetic-upstream', 'synthetic-model', 100, 0, 100, 200, 50, 0.1)`,
				"late-arrival", timezonepolicy.FormatStorage(base.Add(1000*time.Second)))
			if err != nil {
				t.Fatalf("跨连接并发落库失败: %v", err)
			}
		}
	}
	t.Cleanup(func() { tr.onMergeDBBatchLoaded = nil })

	rows, total, err := tr.QueryRequestDetailsWithHotPool(ctx, &QueryOptions{
		RequestFamily: "codex",
		Limit:         20,
		Offset:        240,
	})
	if err != nil {
		t.Fatalf("分页查询失败: %v", err)
	}
	if !injected || batches < 2 {
		t.Fatalf("应触发至少两批查询并注入并发写入: injected=%v batches=%d", injected, batches)
	}
	if total != dbTotal+245 {
		t.Fatalf("total = %d, want %d", total, dbTotal+245)
	}
	if len(rows) != 20 {
		t.Fatalf("返回行数 = %d, want 20", len(rows))
	}

	seen := make(map[string]struct{}, len(rows))
	want := make([]string, 20)
	got := make([]string, 20)
	for i := 0; i < 20; i++ {
		want[i] = fmt.Sprintf("db-%03d", 79-i)
		got[i] = rows[i].RequestID
		if _, dup := seen[got[i]]; dup {
			t.Fatalf("同页出现重复请求 %s: got=%v", got[i], got)
		}
		seen[got[i]] = struct{}{}
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("跨批并发落库时同页结果偏移:\ngot:  %v\nwant: %v", got, want)
	}

	// 新开启的下一次查询应能看到已提交的 late-arrival 排在最前面
	tr.onMergeDBBatchLoaded = nil
	firstPage, newTotal, err := tr.QueryRequestDetailsWithHotPool(ctx, &QueryOptions{
		RequestFamily: "codex",
		Limit:         1,
		Offset:        0,
	})
	if err != nil || newTotal != dbTotal+245+1 || len(firstPage) != 1 || firstPage[0].RequestID != "late-arrival" {
		t.Fatalf("新快照应看到并发落库记录: rows=%+v total=%d err=%v", firstPage, newTotal, err)
	}
}

func TestBuildRequestOverlapCountQuery_UsesRequestIDIndexAndDedup(t *testing.T) {
	tr := newModelUsageTestTracker(t)
	ctx := context.Background()
	now := time.Now()
	opts := modelUsageQueryRange(now)

	query, args, err := buildRequestOverlapCountQuery(opts, requestQuerySourceAll, []string{"hot-1", "hot-2", "hot-3"})
	if err != nil {
		t.Fatal(err)
	}
	rows, err := tr.readDB.QueryContext(ctx, "EXPLAIN QUERY PLAN "+query, args...)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()

	var plan strings.Builder
	for rows.Next() {
		var id, parent, notused int
		var detail string
		if err := rows.Scan(&id, &parent, &notused, &detail); err != nil {
			t.Fatal(err)
		}
		plan.WriteString(detail + "\n")
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	planText := plan.String()
	t.Log(planText)
	if !strings.Contains(planText, "SEARCH request_logs USING INDEX") ||
		(!strings.Contains(planText, "sqlite_autoindex_request_logs_1") && !strings.Contains(planText, "idx_request_logs_request_id")) {
		t.Fatalf("热池查重计数必须走 request_id 索引点查，不能退化为扫描时间索引:\n%s", planText)
	}

	// 验证即使传入重复 ID，也不会重复扣减数据库记录
	seedSessionRequest(t, tr, "dup-id", "codex", "s", now, 100, 0, 50, 0.1)
	seedSessionRequest(t, tr, "keep-id", "codex", "s", now, 100, 0, 50, 0.1)
	count, err := tr.countRequestDetailsExcludingIDs(ctx, tr.readDB, opts, []string{"dup-id", "dup-id", "missing-id"})
	if err != nil {
		t.Fatalf("排除计数失败: %v", err)
	}
	if count != 1 {
		t.Fatalf("重复热池 ID 不应重复扣减: count=%d want=1", count)
	}
}
