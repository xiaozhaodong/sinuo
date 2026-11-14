package service

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"sinuo/internal/store"

	"github.com/fsnotify/fsnotify"
)

type privacyConfigWatcher struct {
	fs     *fsnotify.Watcher
	wake   chan struct{}
	done   chan struct{}
	cancel context.CancelFunc
	mu     sync.Mutex
	err    string
	// 仅启动初始化和监听循环访问；保存实际监听对象，不能把同名新目录当成旧监听。
	directories     map[string]os.FileInfo
	linkPaths       map[string]os.FileInfo
	directoryEvents *privacyDirectoryWatcher
}

func (w *privacyConfigWatcher) setError(message string) { w.mu.Lock(); w.err = message; w.mu.Unlock() }
func (w *privacyConfigWatcher) lastError() string       { w.mu.Lock(); defer w.mu.Unlock(); return w.err }

// StartConfigSourceSync 在入口启动前完成一次同步，然后监听父目录以覆盖原子替换。
// 文件错误写入来源状态，保留上次有效词典；监听器建立失败由调用方显式报告。
func (s *PrivacyService) StartConfigSourceSync(ctx context.Context) error {
	s.mu.Lock()
	if s.sourceWatcher != nil {
		s.mu.Unlock()
		return nil
	}
	fs, err := fsnotify.NewWatcher()
	if err != nil {
		const message = "文件监听启动失败，请重试启动应用"
		s.sourceWatchError = message
		s.mu.Unlock()
		return errors.New(message)
	}
	watchCtx, cancel := context.WithCancel(context.Background())
	w := &privacyConfigWatcher{fs: fs, wake: make(chan struct{}, 1), done: make(chan struct{}), cancel: cancel}
	s.sourceWatcher = w
	s.sourceWatchError = ""
	s.mu.Unlock()
	sources := s.refreshSourceWatches(ctx, w)
	// 同步错误已在各来源内保存，不阻止其他来源和应用启动。
	if err := s.SyncConfigSources(ctx, 0); err != nil {
		slog.Warn("配置来源启动同步未完成", "error", err)
	}
	go s.runConfigSourceWatcher(watchCtx, w, sources)
	return nil
}

// CloseConfigSourceSync 必须在数据库关闭前调用；等待后台读写完全结束。
func (s *PrivacyService) CloseConfigSourceSync() {
	if s == nil {
		return
	}
	s.mu.Lock()
	w := s.sourceWatcher
	s.mu.Unlock()
	if w == nil {
		return
	}
	w.cancel()
	<-w.done
	s.mu.Lock()
	if s.sourceWatcher == w {
		s.sourceWatcher = nil
	}
	s.mu.Unlock()
}

func (s *PrivacyService) wakeConfigSources() {
	s.mu.Lock()
	w := s.sourceWatcher
	s.mu.Unlock()
	if w != nil {
		select {
		case w.wake <- struct{}{}:
		default:
		}
	}
}

func (s *PrivacyService) refreshSourceWatches(ctx context.Context, w *privacyConfigWatcher) []*store.PrivacyConfigSourceRecord {
	sources, err := s.store.ListConfigSources(ctx)
	if err != nil {
		w.setError("无法读取配置来源，请重新启动应用")
		return nil
	}
	wanted := make(map[string]os.FileInfo)
	fileDirectories := make(map[string]bool)
	w.linkPaths = make(map[string]os.FileInfo)
	for _, source := range sources {
		if !source.Enabled {
			continue
		}
		path := filepath.Dir(source.ResolvedPath)
		if source.Kind == "directory" {
			path = source.ResolvedPath
		}
		paths := []string{path}
		if source.Path != source.ResolvedPath {
			paths = append(paths, filepath.Dir(source.Path))
		}
		// 链接可能位于任意祖先，也可能经过登记路径之外的中间链接。
		for link := range privacySourceLinkPaths(source.Path) {
			w.linkPaths[link], _ = os.Lstat(link)
			paths = append(paths, filepath.Dir(link))
		}
		for _, path := range paths {
			addPrivacySourceDirectoryWatches(path, wanted, fileDirectories)
		}
	}
	w.setError("")
	fileWatches := w.fs.WatchList()
	unchanged := len(wanted) == len(w.directories) && len(fileWatches) == len(fileDirectories)
	for _, path := range fileWatches {
		if !fileDirectories[path] {
			unchanged = false
			break
		}
	}
	for path, current := range wanted {
		if previous := w.directories[path]; previous == nil || !os.SameFile(previous, current) {
			unchanged = false
			break
		}
	}
	if unchanged {
		return sources
	}
	// kqueue 在移除重命名目录时会留下隐式子文件监听；重建整个实例才能清除旧对象缓存。
	next, err := fsnotify.NewWatcher()
	if err != nil {
		w.setError("文件监听重建失败，请重新启动应用")
		return sources
	}
	active := make(map[string]os.FileInfo)
	paths := make([]string, 0, len(wanted))
	for path, before := range wanted {
		if fileDirectories[path] {
			if err := next.Add(path); err != nil {
				w.setError("部分目录无法监听，请检查目录权限并重启应用；也可手动立即检查")
				continue
			}
		}
		after, err := os.Stat(path)
		if err != nil || !os.SameFile(before, after) {
			// 安装期间再次替换时不登记错误身份，下一轮移除并重新安装。
			w.setError("监听目录正在变化，将重新检查")
			select {
			case w.wake <- struct{}{}:
			default:
			}
			continue
		}
		active[path] = after
		paths = append(paths, path)
	}
	directoryEvents, err := newPrivacyDirectoryWatcher(paths)
	if err != nil {
		_ = next.Close()
		w.setError("目录变更监听启动失败，请重新启动应用")
		return sources
	}
	previous, previousDirectoryEvents := w.fs, w.directoryEvents
	w.fs, w.directoryEvents = next, directoryEvents
	w.directories = active
	if err := previous.Close(); err != nil {
		w.setError("旧文件监听关闭失败，请重新启动应用")
	}
	previousDirectoryEvents.close()
	return sources
}

// 每一层祖先都可能被整体替换，必须保留到根目录的身份和变更通知。
// macOS 的祖先只用原生目录通知，避免 fsnotify 为无关兄弟文件逐个占用描述符。
func addPrivacySourceDirectoryWatches(path string, directories map[string]os.FileInfo, fileDirectories map[string]bool) {
	nearest := true
	for {
		if info, err := os.Stat(path); err == nil && info.IsDir() {
			if resolved, err := filepath.EvalSymlinks(path); err == nil {
				path = resolved
				directories[path] = info
				if nearest || runtime.GOOS != "darwin" {
					fileDirectories[path] = true
				}
				nearest = false
			}
		}
		parent := filepath.Dir(path)
		if parent == path {
			return
		}
		path = parent
	}
}

// 只收集链接及其目标的路径依赖，不读取目标文件内容，也不改变登记的 ResolvedPath。
// 缺失/断链仍由来源扫描报告；保留链接目标的位置，以便删除后重建时重新检查。
func privacySourceLinkPaths(path string) map[string]bool {
	links := make(map[string]bool)
	visited := make(map[string]bool)
	pending := []string{path}
	add := func(path string) {
		if parent, err := resolvePrivacySourcePath(filepath.Dir(path)); err == nil {
			links[filepath.Join(parent, filepath.Base(path))] = true
		}
	}
	for len(pending) > 0 {
		path := pending[len(pending)-1]
		pending = pending[:len(pending)-1]
		for !visited[path] {
			visited[path] = true
			parent := filepath.Dir(path)
			info, err := os.Lstat(path)
			if os.IsNotExist(err) {
				// 已移除的祖先链接仍需监听其原位置，不能在刷新时丢掉重建通知。
				add(path)
			} else if err == nil && info.Mode()&os.ModeSymlink != 0 {
				add(path)
				if target, err := os.Readlink(path); err == nil {
					if !filepath.IsAbs(target) {
						target = filepath.Join(parent, target)
					}
					target = filepath.Clean(target)
					add(target)
					pending = append(pending, target)
				}
			}
			if parent == path {
				break
			}
			path = parent
		}
	}
	return links
}

func sourceEventRelevant(path string, sources []*store.PrivacyConfigSourceRecord, links map[string]os.FileInfo) bool {
	for link := range links {
		if path == link || strings.HasPrefix(link, path+string(filepath.Separator)) {
			return true
		}
	}
	for _, s := range sources {
		if !s.Enabled {
			continue
		}
		for _, sourcePath := range []string{s.ResolvedPath, s.Path} {
			if path == sourcePath || strings.HasPrefix(sourcePath, path+string(filepath.Separator)) {
				return true
			}
			if s.Kind == "directory" && filepath.Dir(path) == sourcePath && privacySourceMatches(s, filepath.Base(path)) {
				return true
			}
		}
	}
	return false
}

// macOS 的 fsnotify 会过滤目录写事件，同名软链接替换可能没有任何可见事件。
// 原生目录通知只触发身份检查；无关兄弟文件变化不会导致来源重扫。
func (w *privacyConfigWatcher) directoryChanged(path string) bool {
	for directory, previous := range w.directories {
		if directory != path && filepath.Dir(directory) != path {
			continue
		}
		current, _ := os.Stat(directory)
		if current == nil || !os.SameFile(previous, current) {
			return true
		}
	}
	for link, previous := range w.linkPaths {
		if filepath.Dir(link) != path {
			continue
		}
		current, _ := os.Lstat(link)
		if (previous == nil) != (current == nil) || previous != nil && current != nil && !os.SameFile(previous, current) {
			return true
		}
	}
	return false
}

func (s *PrivacyService) runConfigSourceWatcher(ctx context.Context, w *privacyConfigWatcher, sources []*store.PrivacyConfigSourceRecord) {
	defer close(w.done)
	defer func() { _ = w.fs.Close(); w.directoryEvents.close() }()
	// 计时器仅合并短时间内的连续文件事件，不做周期扫描。
	timer := time.NewTimer(time.Hour)
	timer.Stop()
	defer timer.Stop()
	var pending <-chan time.Time
	schedule := func() { timer.Reset(300 * time.Millisecond); pending = timer.C }
	syncSources := func() {
		pending = nil
		timer.Stop()
		sources = s.refreshSourceWatches(ctx, w)
		if err := s.SyncConfigSources(ctx, 0); err != nil && ctx.Err() == nil {
			slog.Warn("配置来源同步未完成", "error", err)
		}
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-w.wake:
			schedule()
		case path, ok := <-w.directoryEvents.events():
			if !ok {
				w.setError("目录变更监听已停止，请重新启动应用")
				return
			}
			if w.directoryChanged(path) {
				schedule()
			}
		case event, ok := <-w.fs.Events:
			if !ok {
				w.setError("文件监听已停止，请重新启动应用")
				return
			}
			if sourceEventRelevant(event.Name, sources, w.linkPaths) {
				schedule()
			}
		case _, ok := <-w.fs.Errors:
			if !ok {
				w.setError("文件监听已停止，请重新启动应用")
				return
			}
			w.setError("文件监听发生错误，将重新扫描登记来源")
			schedule()
		case <-pending:
			syncSources()
		}
	}
}
