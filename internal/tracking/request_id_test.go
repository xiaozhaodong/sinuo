package tracking

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func openIDTestCore(t *testing.T, path string) *CoreDatabase {
	t.Helper()
	core, err := OpenCoreDatabase(DatabaseConfig{Type: "sqlite", DatabasePath: path})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := core.Close(); err != nil {
			t.Error(err)
		}
	})
	if err := core.InitSchema(); err != nil {
		t.Fatal(err)
	}
	return core
}

func requestIDCandidates(ids ...string) func() (string, error) {
	i := 0
	return func() (string, error) {
		if i >= len(ids) {
			return "", fmt.Errorf("unexpected extra candidate request")
		}
		id := ids[i]
		i++
		return id, nil
	}
}

func TestRequestIDReservations_HistoryCleanupAndRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage.db")
	core, err := OpenCoreDatabase(DatabaseConfig{Type: "sqlite", DatabasePath: path})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = core.Close() })
	// 模拟升级前已有归档；占号初始化必须把这些 ID 一并保留。
	if err := core.adapter.InitSchema(); err != nil {
		t.Fatal(err)
	}
	if _, err := core.DB().Exec(`INSERT INTO request_logs (request_id, start_time) VALUES ('req-57b4970a', '2026-09-09T00:00:00.000000Z')`); err != nil {
		t.Fatal(err)
	}
	if err := core.InitSchema(); err != nil {
		t.Fatal(err)
	}
	if _, err := core.DB().Exec(`DELETE FROM request_logs`); err != nil {
		t.Fatal(err)
	}
	core.requestIDs.candidate = requestIDCandidates("req-57b4970a", "req-00000001")
	if id, err := core.NewRequestID(context.Background()); err != nil || id != "req-00000001" {
		t.Fatalf("历史归档清理后仍应拒绝复用 ID: %q, %v", id, err)
	}
	// 归档尚未发生、请求失败或追踪关闭，也必须跨重启保留占号。
	if err := core.Close(); err != nil {
		t.Fatal(err)
	}
	reopened := openIDTestCore(t, path)
	reopened.requestIDs.candidate = requestIDCandidates("req-57b4970a", "req-00000001", "req-00000002")
	if id, err := reopened.NewRequestID(context.Background()); err != nil || id != "req-00000002" {
		t.Fatalf("重启后复用了已发放 ID: %q, %v", id, err)
	}
	// 后续导入的历史 ID 也由同一条占号语句排除。
	if _, err := reopened.DB().Exec(`INSERT INTO request_logs (request_id, start_time) VALUES ('req-00000003', '2026-09-09T00:00:00.000000Z')`); err != nil {
		t.Fatal(err)
	}
	reopened.requestIDs.candidate = requestIDCandidates("req-00000003", "req-00000004")
	if id, err := reopened.NewRequestID(context.Background()); err != nil || id != "req-00000004" {
		t.Fatalf("未排除历史表中的 ID: %q, %v", id, err)
	}
}

func TestRequestIDReservations_ConcurrentIndependentConnections(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage.db")
	cores := []*CoreDatabase{openIDTestCore(t, path), openIDTestCore(t, path)}
	var candidates atomic.Uint32
	generate := func() (string, error) {
		n := candidates.Add(1)
		if n <= 2 {
			return "req-ffffffff", nil
		}
		return fmt.Sprintf("req-%08x", n), nil
	}
	for _, core := range cores {
		core.requestIDs.candidate = generate
	}
	const workers, perWorker = 16, 32
	ids := make(chan string, workers*perWorker)
	failures := make(chan error, workers)
	var wg sync.WaitGroup
	for worker := range workers {
		wg.Go(func() {
			for range perWorker {
				id, err := cores[worker%len(cores)].NewRequestID(context.Background())
				if err != nil {
					failures <- err
					return
				}
				ids <- id
			}
		})
	}
	wg.Wait()
	close(ids)
	close(failures)
	for err := range failures {
		t.Error(err)
	}
	seen := map[string]bool{}
	for id := range ids {
		if seen[id] {
			t.Fatalf("不同连接发放了重复 ID: %s", id)
		}
		seen[id] = true
	}
	if len(seen) != workers*perWorker {
		t.Fatalf("发放数量错误: %d", len(seen))
	}
	if candidates.Load() != workers*perWorker+1 {
		t.Fatalf("碰撞未触发一次重试: %d", candidates.Load())
	}
	var count int
	if err := cores[0].DB().QueryRow(`SELECT COUNT(*) FROM request_id_reservations`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != len(seen) {
		t.Fatalf("返回了未完成占号的 ID: %d vs %d", count, len(seen))
	}
}

func TestRequestIDReservations_DedicatedConnectionAndFormat(t *testing.T) {
	for _, path := range []string{filepath.Join(t.TempDir(), "usage.db"), ":memory:"} {
		t.Run(path, func(t *testing.T) {
			core := openIDTestCore(t, path)
			held, err := core.DB().Conn(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			defer held.Close()
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			id, err := core.NewRequestID(ctx)
			if err != nil {
				t.Fatalf("占号不应等待核心连接池: %v", err)
			}
			if !regexp.MustCompile(`^req-[0-9a-f]{8}$`).MatchString(id) {
				t.Fatalf("格式不是原有 12 字符: %q", id)
			}
			var count int
			if err := held.QueryRowContext(ctx, `SELECT COUNT(*) FROM request_id_reservations WHERE request_id = ?`, id).Scan(&count); err != nil {
				t.Fatal(err)
			}
			if count != 1 {
				t.Fatal("独立连接没有写入同一个数据库")
			}
		})
	}
}

func TestRequestIDReservations_FailuresNeverReturnID(t *testing.T) {
	core := openIDTestCore(t, filepath.Join(t.TempDir(), "usage.db"))
	calls := 0
	core.requestIDs.candidate = func() (string, error) { calls++; return "req-00000001", nil }
	// 除唯一键冲突外的数据库错误必须立即暴露，不能重试或改用无占号 ID。
	if _, err := core.DB().Exec(`CREATE TRIGGER reject_id BEFORE INSERT ON request_id_reservations BEGIN SELECT RAISE(ABORT, 'reservation storage failed'); END;`); err != nil {
		t.Fatal(err)
	}
	if id, err := core.NewRequestID(context.Background()); id != "" || err == nil || !strings.Contains(err.Error(), "reservation storage failed") || calls != 1 {
		t.Fatalf("数据库错误未及时返回: id=%q err=%v calls=%d", id, err, calls)
	}
	if _, err := core.DB().Exec(`DROP TRIGGER reject_id`); err != nil {
		t.Fatal(err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if id, err := core.NewRequestID(canceled); id != "" || !errors.Is(err, context.Canceled) || calls != 1 {
		t.Fatalf("取消后仍然占号: id=%q err=%v calls=%d", id, err, calls)
	}
	// 队列等待遵守调用方超时，独立连接也不能无限等待。
	held, err := core.requestIDs.adapter.db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	id, err := core.NewRequestID(ctx)
	_ = held.Close()
	if id != "" || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("排队超时未返回: %q, %v", id, err)
	}
	if _, err := core.NewRequestID(context.Background()); err != nil {
		t.Fatalf("失败后无法恢复占号: %v", err)
	}
	calls = 0
	if id, err := core.NewRequestID(context.Background()); id != "" || err == nil || calls != REQUEST_ID_MAX_ATTEMPTS {
		t.Fatalf("重复候选不能无限重试: id=%q err=%v calls=%d", id, err, calls)
	}
	if err := core.Close(); err != nil {
		t.Fatal(err)
	}
	if id, err := core.NewRequestID(context.Background()); id != "" || err == nil {
		t.Fatalf("关闭后仍返回了 ID: %q, %v", id, err)
	}
}
