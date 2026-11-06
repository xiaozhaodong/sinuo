package monitor

import (
	"context"
	"fmt"
)

// RequestIDGenerator 必须在返回 ID 前完成持久化占号；由应用统一注入所有入口。
type RequestIDGenerator func(context.Context) (string, error)

func (generate RequestIDGenerator) Next(ctx context.Context) (string, error) {
	if generate == nil {
		return "", fmt.Errorf("request ID allocator is not initialized")
	}
	return generate(ctx)
}
