package tracking

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"

	"github.com/google/uuid"
)

// CoreDatabase 是应用配置与业务存储使用的常驻 SQLite 连接。
// 它不受 usage_tracking.enabled 控制；UsageTracker 仅负责请求追踪生命周期。
type CoreDatabase struct {
	mu         sync.RWMutex
	adapter    DatabaseAdapter
	config     DatabaseConfig
	requestIDs *requestIDAllocator
}

func OpenCoreDatabase(config DatabaseConfig) (*CoreDatabase, error) {
	if config.DatabasePath == ":memory:" {
		// 内存库也使用独立占号连接；名字隔离各 CoreDatabase 的生命周期。
		config.DatabasePath = "file:sinuo-core-" + uuid.NewString() + "?mode=memory&cache=shared"
	}
	adapter, err := NewDatabaseAdapter(config)
	if err != nil {
		return nil, fmt.Errorf("create core database adapter: %w", err)
	}
	if err := adapter.Open(); err != nil {
		return nil, fmt.Errorf("open core database: %w", err)
	}
	return &CoreDatabase{adapter: adapter, config: config}, nil
}

// InitSchema 必须在一次性启动迁移成功后调用。
func (c *CoreDatabase) InitSchema() error {
	if c == nil {
		return fmt.Errorf("core database is nil")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	adapter := c.adapter
	if adapter == nil {
		return fmt.Errorf("core database is closed")
	}
	if err := adapter.InitSchema(); err != nil {
		return fmt.Errorf("initialize core database schema: %w", err)
	}
	if c.requestIDs == nil {
		allocator, err := openRequestIDAllocator(c.config)
		if err != nil {
			return fmt.Errorf("initialize request ID allocator: %w", err)
		}
		c.requestIDs = allocator
	}
	return nil
}

// NewRequestID 原子占号成功后才返回 ID；用量追踪关闭时仍可使用。
func (c *CoreDatabase) NewRequestID(ctx context.Context) (string, error) {
	if c == nil {
		return "", fmt.Errorf("core database is nil")
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.adapter == nil || c.requestIDs == nil {
		return "", fmt.Errorf("request ID allocator is not initialized or is closed")
	}
	return c.requestIDs.next(ctx)
}

func (c *CoreDatabase) DB() *sql.DB {
	if c == nil {
		return nil
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.adapter == nil {
		return nil
	}
	return c.adapter.GetDB()
}

func (c *CoreDatabase) Ping(ctx context.Context) error {
	if c == nil {
		return fmt.Errorf("core database is nil")
	}
	c.mu.RLock()
	adapter := c.adapter
	c.mu.RUnlock()
	if adapter == nil {
		return fmt.Errorf("core database is closed")
	}
	return adapter.Ping(ctx)
}

func (c *CoreDatabase) Close() error {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	adapter := c.adapter
	allocator := c.requestIDs
	c.adapter = nil
	c.requestIDs = nil
	c.mu.Unlock()
	if adapter == nil {
		return nil
	}
	var err error
	if allocator != nil {
		err = allocator.close()
	}
	return errors.Join(err, adapter.Close())
}
