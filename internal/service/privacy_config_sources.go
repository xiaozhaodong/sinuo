package service

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"sinuo/internal/privacy"
	"sinuo/internal/privacy/configimport"
	"sinuo/internal/store"
)

const DefaultPrivacySourcePatterns = ".env,.env.*,.npmrc,.netrc,_netrc,credentials,*.json,*.yaml,*.yml,*.properties"

type PrivacyConfigSourceState struct {
	Sources    []*store.PrivacyConfigSourceRecord `json:"sources"`
	Watching   bool                               `json:"watching"`
	WatchError string                             `json:"watch_error"`
}

func (s *PrivacyService) ListConfigSources(ctx context.Context) (PrivacyConfigSourceState, error) {
	out := PrivacyConfigSourceState{}
	if s == nil || s.store == nil {
		return out, errors.New("隐私服务尚未就绪")
	}
	s.mu.Lock()
	w := s.sourceWatcher
	out.WatchError = s.sourceWatchError
	s.mu.Unlock()
	if w != nil {
		out.Watching = true
		select {
		case <-w.done:
			out.Watching = false
		default:
		}
		out.WatchError = w.lastError()
	}
	var err error
	out.Sources, err = s.store.ListConfigSources(ctx)
	if err != nil {
		return out, errors.New("读取配置来源失败，请重试")
	}
	return out, nil
}

func normalizePrivacyConfigSource(input store.PrivacyConfigSourceRecord, previous *store.PrivacyConfigSourceRecord) (*store.PrivacyConfigSourceRecord, error) {
	r := &store.PrivacyConfigSourceRecord{ID: input.ID, Name: strings.TrimSpace(input.Name), Path: strings.TrimSpace(input.Path), Kind: input.Kind, Patterns: strings.TrimSpace(input.Patterns), Enabled: input.Enabled}
	if r.ID < 0 || r.Path == "" || len(r.Path) > 4096 || strings.ContainsAny(r.Path, "\x00\r\n") {
		return nil, errors.New("请填写有效的本机文件或目录路径")
	}
	if r.Kind != "file" && r.Kind != "directory" {
		return nil, errors.New("来源类型必须为文件或目录")
	}
	if strings.HasPrefix(r.Path, "~/") || r.Path == "~" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, errors.New("无法定位用户目录")
		}
		r.Path = filepath.Join(home, strings.TrimPrefix(strings.TrimPrefix(r.Path, "~"), "/"))
	}
	if !filepath.IsAbs(r.Path) {
		return nil, errors.New("请使用绝对路径或 ~/ 开头的路径")
	}
	r.Path = filepath.Clean(r.Path)
	// 保留登记路径的文件名用于格式识别；读取和监听固定到登记时解析的实际位置。
	if previous != nil && previous.Path == r.Path && previous.Kind == r.Kind {
		// 改名或暂停不重新授权链接的新目标，链接失效时也必须能暂停来源。
		r.ResolvedPath = previous.ResolvedPath
	} else {
		resolved, err := resolvePrivacySourcePath(r.Path)
		if err != nil {
			return nil, errors.New("无法访问来源路径，请检查权限")
		}
		r.ResolvedPath = resolved
	}
	if r.Name == "" {
		r.Name = filepath.Base(r.Path)
	}
	if len(r.Name) > 120 || strings.ContainsAny(r.Name, "\x00\r\n") {
		return nil, errors.New("来源名称不能超过 120 字节或包含换行")
	}
	if r.Kind == "file" {
		if !configimport.SupportsFileName(r.Path) {
			return nil, errors.New("该文件名不属于支持的配置格式")
		}
		r.Patterns = ""
	} else {
		if r.Patterns == "" {
			r.Patterns = DefaultPrivacySourcePatterns
		}
		if len(r.Patterns) > 1024 || len(strings.Split(r.Patterns, ",")) > 20 {
			return nil, errors.New("目录文件模式过多或过长")
		}
		var patterns []string
		for _, pattern := range strings.Split(r.Patterns, ",") {
			pattern = strings.TrimSpace(pattern)
			if pattern == "" || strings.ContainsAny(pattern, "/\\\x00\r\n") {
				return nil, errors.New("目录模式只匹配当前层级文件名，请使用逗号分隔")
			}
			if _, err := filepath.Match(pattern, "sample"); err != nil {
				return nil, errors.New("目录文件模式无效")
			}
			patterns = append(patterns, pattern)
		}
		r.Patterns = strings.Join(patterns, ",")
	}
	return r, nil
}

// 来源尚不存在时规范化已存在的祖先，再拼接缺失尾部。
// 已存在但断开的软链接不能被当成缺失目录，以免静默固定到错误位置。
func resolvePrivacySourcePath(path string) (string, error) {
	ancestor := path
	var suffix []string
	for {
		resolved, err := filepath.EvalSymlinks(ancestor)
		if err == nil {
			if len(suffix) > 0 {
				info, err := os.Stat(resolved)
				if err != nil {
					return "", err
				}
				if !info.IsDir() {
					return "", errors.New("来源父路径不是目录")
				}
			}
			for i := len(suffix) - 1; i >= 0; i-- {
				resolved = filepath.Join(resolved, suffix[i])
			}
			return resolved, nil
		}
		if !os.IsNotExist(err) {
			return "", err
		}
		if _, statErr := os.Lstat(ancestor); !os.IsNotExist(statErr) {
			return "", err
		}
		parent := filepath.Dir(ancestor)
		if parent == ancestor {
			return "", err
		}
		suffix = append(suffix, filepath.Base(ancestor))
		ancestor = parent
	}
}

func (s *PrivacyService) SaveConfigSource(ctx context.Context, input store.PrivacyConfigSourceRecord) (*store.PrivacyConfigSourceRecord, error) {
	s.sourceMu.Lock()
	defer s.sourceMu.Unlock()
	all, err := s.store.ListConfigSources(ctx)
	if err != nil {
		return nil, errors.New("读取配置来源失败")
	}
	var previous *store.PrivacyConfigSourceRecord
	for _, old := range all {
		if old.ID == input.ID {
			previous = old
			break
		}
	}
	r, err := normalizePrivacyConfigSource(input, previous)
	if err != nil {
		return nil, err
	}
	found := r.ID == 0
	for _, old := range all {
		if old.ID == r.ID {
			found = true
		}
		if (old.Path == r.Path || old.ResolvedPath == r.ResolvedPath) && old.ID != r.ID {
			return nil, errors.New("该路径已登记，请修改已有来源")
		}
	}
	if !found {
		return nil, errors.New("配置来源已不存在，请刷新")
	}
	saved, err := s.store.SaveConfigSource(ctx, r)
	if err != nil {
		return nil, errors.New("保存配置来源失败，请重试")
	}
	s.wakeConfigSources()
	return saved, nil
}

func (s *PrivacyService) DeleteConfigSource(ctx context.Context, id int64) error {
	if id <= 0 {
		return errors.New("配置来源 ID 无效")
	}
	s.sourceMu.Lock()
	defer s.sourceMu.Unlock()
	if err := s.store.DeleteConfigSource(ctx, id); err != nil {
		return errors.New("移除配置来源失败，请刷新后重试")
	}
	s.wakeConfigSources()
	return nil
}

// SyncConfigSources 可同步全部或一个来源；单来源失败可见且不阻止其他来源同步。
func (s *PrivacyService) SyncConfigSources(ctx context.Context, id int64) error {
	if id < 0 {
		return errors.New("配置来源 ID 无效")
	}
	s.sourceMu.Lock()
	defer s.sourceMu.Unlock()
	all, err := s.store.ListConfigSources(ctx)
	if err != nil {
		return errors.New("读取配置来源失败")
	}
	found := id == 0
	var failures []error
	for _, r := range all {
		if id != 0 && r.ID != id {
			continue
		}
		found = true
		if !r.Enabled {
			continue
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := s.syncConfigSource(ctx, r); err != nil {
			failures = append(failures, err)
		}
	}
	if !found {
		return errors.New("配置来源已不存在，请刷新")
	}
	return errors.Join(failures...)
}

func (s *PrivacyService) syncConfigSource(ctx context.Context, r *store.PrivacyConfigSourceRecord) error {
	r.LastScanAt = time.Now().UTC().Format(time.RFC3339Nano)
	values, digest, err := readPrivacyConfigSource(ctx, r)
	if err == nil && digest == r.ContentHash {
		// 不重编译或重新登记已删除值；失败后恢复原内容时清除错误。
		if err := s.store.SetConfigSourceError(ctx, r.ID, r.LastScanAt, ""); err != nil {
			return errors.New("无法更新来源检查状态，请重试")
		}
		return nil
	}
	if err == nil {
		r.ContentHash = digest
		s.mu.Lock()
		var next *privacy.Snapshot
		_, err = s.store.SyncConfigSource(ctx, r, values, func(records []*store.PrivacyExactSecretRecord) error {
			var e error
			next, e = s.snapshotWithExactSecretsLocked(records)
			return e
		})
		if err == nil && next != nil {
			s.publishSnapshotLocked(next)
		}
		s.mu.Unlock()
		if err != nil {
			switch {
			case errors.Is(err, privacy.ErrLiteralBudget):
				err = privacy.ErrLiteralBudget
			case errors.Is(err, errPrivacyConfigDictionaryLimit):
				err = errPrivacyConfigDictionaryLimit
			case errors.Is(err, store.ErrConfigSourceHistoryLimit):
				err = store.ErrConfigSourceHistoryLimit
			default:
				err = errors.New("同步写入失败，本次未生效，请重试")
			}
		}
	}
	if err != nil {
		// 文件/解析错误均由扫描器转换为固定原因，日志与状态不携带原始内容。
		if saveErr := s.store.SetConfigSourceError(ctx, r.ID, r.LastScanAt, err.Error()); saveErr != nil {
			return errors.New("来源同步失败，且无法保存失败状态")
		}
	}
	return err
}
