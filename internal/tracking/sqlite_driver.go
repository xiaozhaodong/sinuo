package tracking

import (
	"context"
	"database/sql/driver"
	"fmt"
	"strings"

	"modernc.org/sqlite"
)

// sqliteDSNParams 运行时连接参数。modernc 驱动只识别 _pragma / _txlock / _time_format，
// mattn 风格的 _busy_timeout / _journal_mode 会被静默忽略（此前的参数从未生效）。
//   - busy_timeout 5s：CoreDatabase 与 UsageTracker 两个连接池写同一文件，冲突时等待而非立即失败；
//     每个池只有一条连接，等锁期间整池排队，不宜过长
//   - journal_mode=WAL：读写互不阻塞，外部读者也挡不住提交
//   - journal_size_limit 64MB：VACUUM 后把 -wal 文件回缩
//   - _txlock=immediate：写事务开头即取写锁；WAL 下先读后写的事务遇到快照过期会直接 BUSY、不走 busy 等待。
//     modernc 对只读事务仍用普通 BEGIN
const sqliteDSNParams = "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)" +
	"&_pragma=journal_size_limit(67108864)&_txlock=immediate"

// sqliteConnector 包装 modernc 连接：事务提交失败时立即回滚。
// SQLite 的 COMMIT 返回 SQLITE_BUSY 时事务仍处于打开状态，而 database/sql 在 Commit 出错后
// 就视事务为结束（之后的 tx.Rollback 是空操作）并把连接放回池中；池里只有一条连接时，
// 之后所有写入都会报 "cannot start a transaction within a transaction"，直到进程重启。
type sqliteConnector struct {
	dsn    string
	driver *sqlite.Driver
}

func newSQLiteConnector(dsn string) *sqliteConnector {
	return &sqliteConnector{dsn: dsn, driver: &sqlite.Driver{}}
}

func (c *sqliteConnector) Connect(context.Context) (driver.Conn, error) {
	conn, err := c.driver.Open(c.dsn)
	if err != nil {
		return nil, err
	}
	inner, ok := conn.(sqliteDriverConn)
	if !ok {
		_ = conn.Close()
		return nil, fmt.Errorf("unexpected sqlite driver connection type %T", conn)
	}
	return &guardedSQLiteConn{sqliteDriverConn: inner}, nil
}

func (c *sqliteConnector) Driver() driver.Driver {
	return c.driver
}

// sqliteDriverConn 是 modernc 连接实现的全部接口；嵌入它才能把可选接口原样转发给 database/sql
type sqliteDriverConn interface {
	driver.Conn
	driver.ConnBeginTx
	driver.ConnPrepareContext
	driver.ExecerContext
	driver.QueryerContext
	driver.Pinger
	driver.SessionResetter
	driver.Validator
}

// guardedSQLiteConn 的方法都由 database/sql 在持有连接锁时调用，broken 无需额外同步
type guardedSQLiteConn struct {
	sqliteDriverConn
	broken bool
}

func (c *guardedSQLiteConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	tx, err := c.sqliteDriverConn.BeginTx(ctx, opts)
	if err != nil {
		return nil, err
	}
	return &guardedSQLiteTx{Tx: tx, conn: c}, nil
}

func (c *guardedSQLiteConn) ResetSession(ctx context.Context) error {
	if c.broken {
		return driver.ErrBadConn
	}
	return c.sqliteDriverConn.ResetSession(ctx)
}

func (c *guardedSQLiteConn) IsValid() bool {
	return !c.broken && c.sqliteDriverConn.IsValid()
}

type guardedSQLiteTx struct {
	driver.Tx
	conn *guardedSQLiteConn
}

// Commit 失败时立即回滚，释放锁并结束事务；调用方拿到的仍是提交错误（按未写入处理）
func (t *guardedSQLiteTx) Commit() error {
	err := t.Tx.Commit()
	if err == nil {
		return nil
	}
	if rollbackErr := t.Tx.Rollback(); rollbackErr != nil && !isNoActiveTransactionError(rollbackErr) {
		// 连接上的事务状态已不可信：交给 database/sql 丢弃，关闭连接时 SQLite 会自动回滚
		t.conn.broken = true
	}
	return err
}

// isNoActiveTransactionError SQLite 已在提交失败时自动回滚的情况
func isNoActiveTransactionError(err error) bool {
	return strings.Contains(err.Error(), "no transaction is active")
}
