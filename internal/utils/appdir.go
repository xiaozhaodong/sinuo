package utils

import (
	"os"
	"path/filepath"

	"cc-forwarder/internal/appdir"
)

// GetAppDataDir 获取应用数据目录（跨平台）
func GetAppDataDir() string {
	return appdir.GetAppDataDir()
}

// GetLegacyAppDataDir 获取品牌更名前（CC-Forwarder 时代）的数据目录，仅用于迁移
func GetLegacyAppDataDir() string {
	return appdir.GetLegacyAppDataDir()
}

// MigrateLegacyAppDataDir 将旧品牌数据目录一次性迁移到新目录。
// 迁移规则与侧车保护统一由 appdir 维护；出错时调用方必须停止启动。
func MigrateLegacyAppDataDir() (bool, error) {
	return appdir.MigrateLegacyAppDataDir()
}

// GetDataDir 获取数据库目录
func GetDataDir() string {
	return filepath.Join(GetAppDataDir(), "data")
}

// GetLogDir 获取日志目录
func GetLogDir() string {
	return filepath.Join(GetAppDataDir(), "logs")
}

// GetConfigDir 获取配置目录
func GetConfigDir() string {
	return filepath.Join(GetAppDataDir(), "config")
}

// EnsureAppDirs 确保应用所需的所有目录存在
func EnsureAppDirs() error {
	dirs := []string{
		GetAppDataDir(),
		GetDataDir(),
		GetLogDir(),
		GetConfigDir(),
	}

	for _, dir := range dirs {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return err
		}
	}

	return nil
}
