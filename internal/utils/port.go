// Package utils 提供通用工具函数
// 端口探测工具 - v5.1.0 新增 (2025-12-08)
package utils

import (
	"fmt"
	"log/slog"
	"net"
	"sync"
)

// PortInfo 端口信息
type PortInfo struct {
	PreferredPort   int  `json:"preferred_port"`    // 用户设置的首选端口
	ActualPort      int  `json:"actual_port"`       // 实际使用的端口
	IsDefault       bool `json:"is_default"`        // 是否使用默认端口
	WasOccupied     bool `json:"was_occupied"`      // 首选端口是否被占用
	LANAccessActive bool `json:"lan_access_active"` // 本次启动实际监听范围
}

// PortManager 端口管理器
type PortManager struct {
	mu            sync.RWMutex
	preferredPort int
	actualPort    int
	wasOccupied   bool
	lanAccess     bool
}

// NewPortManager 创建端口管理器
func NewPortManager(preferredPort int) *PortManager {
	return &PortManager{
		preferredPort: preferredPort,
	}
}

// FindAvailablePort 从首选端口开始，查找可用端口
// maxAttempts: 最多尝试次数（从首选端口开始递增）
// 返回可用端口号，如果找不到返回错误
func FindAvailablePort(preferred int, maxAttempts int) (int, error) {
	if maxAttempts <= 0 {
		maxAttempts = 10
	}

	for i := 0; i < maxAttempts; i++ {
		port := preferred + i
		if IsPortAvailable(port) {
			if i > 0 {
				slog.Warn(fmt.Sprintf("⚠️ 端口 %d 被占用，自动使用端口 %d", preferred, port))
			}
			return port, nil
		}
	}

	return 0, fmt.Errorf("无法找到可用端口 (尝试范围: %d-%d)", preferred, preferred+maxAttempts-1)
}

// IsPortAvailable 检查端口是否可用
func IsPortAvailable(port int) bool {
	addr := fmt.Sprintf(":%d", port)
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return false
	}
	listener.Close()
	return true
}

// FindAndBind 查找可用端口并绑定。lanAccess=true 时绑定所有网卡；否则
// 同时绑定 IPv4/IPv6 回环（本机没有 IPv6 回环时只绑定 IPv4）。
func FindAndBind(preferred int, maxAttempts int, lanAccess bool) ([]net.Listener, int, error) {
	if maxAttempts <= 0 {
		maxAttempts = 10
	}
	ipv6Loopback := false
	if !lanAccess {
		probe, err := net.Listen("tcp6", "[::1]:0")
		if err == nil {
			ipv6Loopback = true
			_ = probe.Close()
		}
	}

	for i := 0; i < maxAttempts; i++ {
		port := preferred + i
		listeners, ok := bindPort(port, lanAccess, ipv6Loopback)
		if ok {
			if i > 0 {
				slog.Warn(fmt.Sprintf("⚠️ 端口 %d 被占用，自动使用端口 %d", preferred, port))
			}
			return listeners, port, nil
		}
	}

	return nil, 0, fmt.Errorf("无法找到可用端口 (尝试范围: %d-%d)", preferred, preferred+maxAttempts-1)
}

func bindPort(port int, lanAccess, ipv6Loopback bool) ([]net.Listener, bool) {
	if lanAccess {
		listener, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
		if err != nil {
			return nil, false
		}
		return []net.Listener{listener}, true
	}

	// Probe the wildcard address first so a process occupying only one address
	// family cannot make this process claim the same port in pieces.
	probe, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
	if err != nil {
		return nil, false
	}
	_ = probe.Close()

	ipv4, err := net.Listen("tcp4", net.JoinHostPort("127.0.0.1", fmt.Sprintf("%d", port)))
	if err != nil {
		return nil, false
	}
	listeners := []net.Listener{ipv4}
	if ipv6Loopback {
		ipv6, err := net.Listen("tcp6", net.JoinHostPort("::1", fmt.Sprintf("%d", port)))
		if err != nil {
			_ = ipv4.Close()
			return nil, false
		}
		listeners = append(listeners, ipv6)
	}
	return listeners, true
}

// SetPreferredPort 设置首选端口
func (pm *PortManager) SetPreferredPort(port int) {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	pm.preferredPort = port
}

// SetActualPort 设置实际使用的端口
func (pm *PortManager) SetActualPort(port int) {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	pm.actualPort = port
	pm.wasOccupied = port != pm.preferredPort
}

// SetLANAccessActive records the listener scope selected at startup.
func (pm *PortManager) SetLANAccessActive(enabled bool) {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	pm.lanAccess = enabled
}

// GetPreferredPort 获取首选端口
func (pm *PortManager) GetPreferredPort() int {
	pm.mu.RLock()
	defer pm.mu.RUnlock()
	return pm.preferredPort
}

// GetActualPort 获取实际端口
func (pm *PortManager) GetActualPort() int {
	pm.mu.RLock()
	defer pm.mu.RUnlock()
	return pm.actualPort
}

// LANAccessActive reports the listener scope selected at startup.
func (pm *PortManager) LANAccessActive() bool {
	pm.mu.RLock()
	defer pm.mu.RUnlock()
	return pm.lanAccess
}

// GetPortInfo 获取完整端口信息
func (pm *PortManager) GetPortInfo() PortInfo {
	pm.mu.RLock()
	defer pm.mu.RUnlock()
	return PortInfo{
		PreferredPort:   pm.preferredPort,
		ActualPort:      pm.actualPort,
		IsDefault:       pm.preferredPort == 8087, // 默认端口
		WasOccupied:     pm.wasOccupied,
		LANAccessActive: pm.lanAccess,
	}
}

// FindAndSetPort 查找可用端口并设置
func (pm *PortManager) FindAndSetPort(maxAttempts int) (int, error) {
	port, err := FindAvailablePort(pm.preferredPort, maxAttempts)
	if err != nil {
		return 0, err
	}
	pm.SetActualPort(port)
	return port, nil
}
