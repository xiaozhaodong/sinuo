package tracking

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"time"
)

const REQUEST_ID_ALLOCATION_TIMEOUT = 5 * time.Second
const REQUEST_ID_MAX_ATTEMPTS = 128

// requestIDAllocator 的连接只用于占号，不与归档、统计共用连接池。
// 占号记录不随请求归档、失败或过期清理而删除，避免重启后再次使用同一个 ID。
type requestIDAllocator struct {
	adapter   *SQLiteAdapter
	reserve   *sql.Stmt
	candidate func() (string, error)
}

func openRequestIDAllocator(config DatabaseConfig) (*requestIDAllocator, error) {
	adapter, err := NewSQLiteAdapter(config)
	if err != nil {
		return nil, err
	}
	if err := adapter.Open(); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	// 一次事务建表并保留历史 ID；重复初始化幂等，也兼容恢复的历史数据。
	tx, err := adapter.BeginTx(ctx, nil)
	if err == nil {
		_, err = tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS request_id_reservations (
			request_id TEXT PRIMARY KEY NOT NULL
		) WITHOUT ROWID;
		INSERT INTO request_id_reservations (request_id)
		SELECT request_id FROM request_logs WHERE true
		ON CONFLICT(request_id) DO NOTHING;`)
		if err == nil {
			err = tx.Commit()
		} else {
			err = errors.Join(err, tx.Rollback())
		}
	}
	if err != nil {
		return nil, errors.Join(fmt.Errorf("initialize request ID reservations: %w", err), adapter.Close())
	}
	// 历史表检查和占号在同一条写语句内执行；不使用先 SELECT 再 INSERT 的竞态流程。
	stmt, err := adapter.db.PrepareContext(ctx, `INSERT INTO request_id_reservations (request_id)
		SELECT ? WHERE NOT EXISTS (SELECT 1 FROM request_logs WHERE request_id = ?)
		ON CONFLICT(request_id) DO NOTHING`)
	if err != nil {
		return nil, errors.Join(fmt.Errorf("prepare request ID reservation: %w", err), adapter.Close())
	}
	return &requestIDAllocator{adapter: adapter, reserve: stmt, candidate: randomRequestID}, nil
}

func randomRequestID() (string, error) {
	var bytes [4]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return "", fmt.Errorf("generate request ID: %w", err)
	}
	return "req-" + hex.EncodeToString(bytes[:]), nil
}

func (a *requestIDAllocator) next(ctx context.Context) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, REQUEST_ID_ALLOCATION_TIMEOUT)
	defer cancel()
	for range REQUEST_ID_MAX_ATTEMPTS {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		id, err := a.candidate()
		if err != nil {
			return "", err
		}
		result, err := a.reserve.ExecContext(ctx, id, id)
		if err != nil {
			return "", fmt.Errorf("reserve request ID: %w", err)
		}
		rows, err := result.RowsAffected()
		if err != nil {
			return "", fmt.Errorf("read request ID reservation result: %w", err)
		}
		if rows == 1 {
			return id, nil
		}
	}
	return "", fmt.Errorf("request ID allocation exceeded %d collision retries", REQUEST_ID_MAX_ATTEMPTS)
}

func (a *requestIDAllocator) close() error {
	return errors.Join(a.reserve.Close(), a.adapter.Close())
}
