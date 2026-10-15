// Package appdir 统一解析应用目录，并在业务启动前迁移旧品牌数据；仅依赖标准库。
package appdir

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

const (
	DesktopName  = "Sinuo"
	UnixName     = "sinuo"
	FallbackName = ".sinuo"

	LegacyDesktopName  = "AI-Switchboard"
	legacyUnixName     = "ai-switchboard"
	legacyFallbackName = ".ai-switchboard"
	olderDesktopName   = "CC-Forwarder"
	olderUnixName      = "cc-forwarder"
	olderFallbackName  = ".cc-forwarder"
)

// GetAppDataDir 只解析目录，不创建目录或打开数据库。
func GetAppDataDir() string {
	return currentPlatformDir(DesktopName, UnixName, FallbackName)
}

// GetLegacyAppDataDir 保留最早品牌目录的查询语义，仅用于兼容旧调用方。
func GetLegacyAppDataDir() string {
	return currentPlatformDir(olderDesktopName, olderUnixName, olderFallbackName)
}

func currentPlatformDir(desktopName, unixName, fallbackName string) string {
	homeDir, _ := os.UserHomeDir()
	return resolveAppDataDir(runtime.GOOS, homeDir, desktopName, unixName, fallbackName)
}

func resolveAppDataDir(goos, homeDir, desktopName, unixName, fallbackName string) string {
	switch goos {
	case "windows":
		baseDir := os.Getenv("APPDATA")
		if baseDir == "" {
			baseDir = filepath.Join(os.Getenv("USERPROFILE"), "AppData", "Roaming")
		}
		return filepath.Join(baseDir, desktopName)
	case "darwin":
		return filepath.Join(homeDir, "Library", "Application Support", desktopName)
	case "linux":
		if baseDir := os.Getenv("XDG_DATA_HOME"); baseDir != "" {
			return filepath.Join(baseDir, unixName)
		}
		return filepath.Join(homeDir, ".local", "share", unixName)
	default:
		return filepath.Join(homeDir, fallbackName)
	}
}

// MigrateLegacyAppDataDir 在目标不存在时，按品牌新旧顺序整体 rename。
// 失败时不创建目标目录；调用方必须终止启动，不能继续初始化配置、数据库或监听器。
func MigrateLegacyAppDataDir() (bool, error) {
	newDir := GetAppDataDir()
	if _, err := os.Lstat(newDir); err == nil {
		return false, nil
	} else if !os.IsNotExist(err) {
		return false, fmt.Errorf("检查目标数据目录 %s: %w", newDir, err)
	}
	oldDirs := []string{
		currentPlatformDir(LegacyDesktopName, legacyUnixName, legacyFallbackName),
		GetLegacyAppDataDir(),
	}
	for _, oldDir := range oldDirs {
		info, err := os.Lstat(oldDir)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return false, fmt.Errorf("检查旧数据目录 %s: %w", oldDir, err)
		}
		if !info.IsDir() {
			return false, fmt.Errorf("旧数据路径不是目录: %s", oldDir)
		}
		// 只认正在使用的数据库侧车，不能按后缀匹配备份残留。
		for _, name := range []string{"usage.db-wal", "usage.db-shm"} {
			path := filepath.Join(oldDir, "data", name)
			if _, err := os.Lstat(path); err == nil {
				return false, fmt.Errorf("旧数据库侧车文件仍存在，已阻止迁移: %s", path)
			} else if !os.IsNotExist(err) {
				return false, fmt.Errorf("检查旧数据库侧车文件 %s: %w", path, err)
			}
		}
		if err := os.Rename(oldDir, newDir); err != nil {
			return false, fmt.Errorf("迁移数据目录 %s -> %s: %w", oldDir, newDir, err)
		}
		return true, nil
	}
	return false, nil
}
