package tracking

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"path/filepath"
	"reflect"
	"testing"
)

func openTestSQLiteAdapter(t *testing.T, path string) *SQLiteAdapter {
	t.Helper()
	adapter, err := NewSQLiteAdapter(DatabaseConfig{DatabasePath: path})
	if err != nil {
		t.Fatal(err)
	}
	if err := adapter.Open(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = adapter.Close() })
	return adapter
}

func execOrFatal(t *testing.T, db *sql.DB, query string) {
	t.Helper()
	if _, err := db.Exec(query); err != nil {
		t.Fatalf("exec %q: %v", query, err)
	}
}

func TestSQLiteAdapterOpen_AppliesConnectionPragmas(t *testing.T) {
	adapter := openTestSQLiteAdapter(t, filepath.Join(t.TempDir(), "usage.db"))

	var mode string
	if err := adapter.db.QueryRow("PRAGMA journal_mode").Scan(&mode); err != nil {
		t.Fatal(err)
	}
	var busyTimeout int
	if err := adapter.db.QueryRow("PRAGMA busy_timeout").Scan(&busyTimeout); err != nil {
		t.Fatal(err)
	}
	if mode != "wal" || busyTimeout != 5000 {
		t.Fatalf("journal_mode=%q busy_timeout=%d, want wal / 5000", mode, busyTimeout)
	}
}

// 2026-09-28 事故：外部读者占着读锁时 App 提交失败，连接残留事务导致之后的写入全部失败
func TestSQLiteAdapter_ReaderDoesNotBlockCommit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage.db")
	writer := openTestSQLiteAdapter(t, path)
	reader := openTestSQLiteAdapter(t, path)
	execOrFatal(t, writer.db, "CREATE TABLE t (v INTEGER)")
	execOrFatal(t, writer.db, "INSERT INTO t VALUES (1)")

	// 未读完的结果集持有读事务
	rows, err := reader.db.Query("SELECT v FROM t")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	if !rows.Next() {
		t.Fatal("expected a row")
	}

	tx, err := writer.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec("INSERT INTO t VALUES (2)"); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit blocked by reader: %v", err)
	}

	next, err := writer.db.Begin()
	if err != nil {
		t.Fatalf("connection left in open transaction: %v", err)
	}
	_ = next.Rollback()
}

// 非 WAL 下提交仍会被读者挡住：失败后连接必须回到可用状态，失败的写入不能被后续事务顺带提交
func TestGuardedSQLiteTx_RecoversAfterCommitBusy(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rollback-journal.db")
	openDB := func() *sql.DB {
		db := sql.OpenDB(newSQLiteConnector(path + "?_pragma=busy_timeout(50)"))
		db.SetMaxOpenConns(1)
		t.Cleanup(func() { _ = db.Close() })
		return db
	}
	writer, reader := openDB(), openDB()
	execOrFatal(t, writer, "CREATE TABLE t (v INTEGER)")
	execOrFatal(t, writer, "INSERT INTO t VALUES (1)")

	rows, err := reader.Query("SELECT v FROM t")
	if err != nil {
		t.Fatal(err)
	}
	if !rows.Next() {
		t.Fatal("expected a row")
	}
	tx, err := writer.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec("INSERT INTO t VALUES (2)"); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err == nil {
		t.Fatal("expected commit to fail while reader holds the shared lock")
	}
	_ = rows.Close()

	retry, err := writer.Begin()
	if err != nil {
		t.Fatalf("connection left in open transaction: %v", err)
	}
	if _, err := retry.Exec("INSERT INTO t VALUES (3)"); err != nil {
		t.Fatal(err)
	}
	if err := retry.Commit(); err != nil {
		t.Fatal(err)
	}

	var values []int
	result, err := writer.Query("SELECT v FROM t ORDER BY v")
	if err != nil {
		t.Fatal(err)
	}
	defer result.Close()
	for result.Next() {
		var v int
		if err := result.Scan(&v); err != nil {
			t.Fatal(err)
		}
		values = append(values, v)
	}
	if !reflect.DeepEqual(values, []int{1, 3}) {
		t.Fatalf("rows = %v, want [1 3]", values)
	}
}

type fakeDriverTx struct {
	commitErr   error
	rollbackErr error
}

func (f fakeDriverTx) Commit() error   { return f.commitErr }
func (f fakeDriverTx) Rollback() error { return f.rollbackErr }

func TestGuardedSQLiteTx_DiscardsConnWhenRollbackFails(t *testing.T) {
	commitErr := errors.New("disk I/O error")
	cases := []struct {
		name        string
		rollbackErr error
		wantBroken  bool
	}{
		{name: "rollback succeeded", wantBroken: false},
		{name: "already rolled back", rollbackErr: errors.New("SQL logic error: cannot rollback - no transaction is active (1)"), wantBroken: false},
		{name: "rollback failed", rollbackErr: errors.New("disk I/O error"), wantBroken: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			conn := &guardedSQLiteConn{}
			tx := &guardedSQLiteTx{Tx: fakeDriverTx{commitErr: commitErr, rollbackErr: tc.rollbackErr}, conn: conn}
			if err := tx.Commit(); !errors.Is(err, commitErr) {
				t.Fatalf("Commit() = %v, want commit error", err)
			}
			if conn.broken != tc.wantBroken {
				t.Fatalf("broken = %v, want %v", conn.broken, tc.wantBroken)
			}
			if !tc.wantBroken {
				return
			}
			if conn.IsValid() {
				t.Fatal("broken connection must not return to the pool")
			}
			if err := conn.ResetSession(context.Background()); !errors.Is(err, driver.ErrBadConn) {
				t.Fatalf("ResetSession() = %v, want driver.ErrBadConn", err)
			}
		})
	}
}
