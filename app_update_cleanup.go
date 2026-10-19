package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"time"
)

var appUpdateTempFilePattern = regexp.MustCompile(`^wails-update-([0-9]+\.log|[0-9]+-[0-9]+\.ready)$`)
var appUpdateTempDirPattern = regexp.MustCompile(`^wails-update-[0-9]+$`)

func (m *appUpdateManager) scheduleUpdateCleanup() {
	go func() {
		timer := time.NewTimer(5 * time.Minute)
		defer timer.Stop()
		select {
		case <-m.rootCtx.Done():
			return
		case <-timer.C:
		}
		if err := cleanupAppUpdateTemp(m.rootCtx, os.TempDir(), time.Now()); err != nil {
			m.logger.Warn("清理过期软件更新临时文件失败", "error", err)
		}
	}()
}

func cleanupAppUpdateTemp(ctx context.Context, dir string, now time.Time) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	var failures []error
	for _, entry := range entries {
		if ctx.Err() != nil {
			return errors.Join(append(failures, ctx.Err())...)
		}
		// 不跟随符号链接，不递归扫描临时根目录，只删除精确白名单条目。
		if entry.Type()&os.ModeSymlink != 0 {
			continue
		}
		if entry.IsDir() {
			if !appUpdateTempDirPattern.MatchString(entry.Name()) {
				continue
			}
		} else if !appUpdateTempFilePattern.MatchString(entry.Name()) {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			failures = append(failures, err)
			continue
		}
		if now.Sub(info.ModTime()) <= 24*time.Hour {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		if entry.IsDir() {
			err = os.RemoveAll(path)
		} else {
			err = os.Remove(path)
		}
		if err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}
