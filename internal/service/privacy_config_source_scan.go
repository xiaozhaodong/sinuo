package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"sinuo/internal/privacy/configimport"
	"sinuo/internal/store"
)

func privacySourceMatches(r *store.PrivacyConfigSourceRecord, name string) bool {
	if !configimport.SupportsFileName(name) {
		return false
	}
	for _, pattern := range strings.Split(r.Patterns, ",") {
		if ok, _ := filepath.Match(pattern, name); ok {
			return true
		}
	}
	return false
}

func privacySourceFiles(r *store.PrivacyConfigSourceRecord) ([]string, error) {
	logicalTarget, err := filepath.EvalSymlinks(r.Path)
	if err != nil {
		return nil, errors.New("登记路径不存在或无法访问，请检查路径和权限；已导入值保留")
	}
	if logicalTarget != r.ResolvedPath {
		return nil, errors.New("登记路径的链接目标已改变，请移除后重新登记；已导入值保留")
	}
	info, err := os.Lstat(r.ResolvedPath)
	if err != nil {
		return nil, errors.New("来源不存在或无法读取，请检查路径和权限；已导入值保留")
	}
	resolved, err := filepath.EvalSymlinks(r.ResolvedPath)
	if err != nil || resolved != r.ResolvedPath {
		return nil, errors.New("来源路径已变为软链接，请重新登记实际路径")
	}
	if r.Kind == "file" {
		if !info.Mode().IsRegular() {
			return nil, errors.New("文件来源必须是普通文件")
		}
		return []string{r.ResolvedPath}, nil
	}
	if !info.IsDir() {
		return nil, errors.New("目录来源不再是目录")
	}
	dir, err := os.Open(r.ResolvedPath)
	if err != nil {
		return nil, errors.New("无法读取来源目录")
	}
	defer dir.Close()
	var files []string
	// 限制枚举量，避免把巨大目录整体载入内存。
	for count := 0; ; {
		entries, err := dir.ReadDir(128)
		if err != nil && err != io.EOF {
			return nil, errors.New("读取来源目录失败")
		}
		count += len(entries)
		if count > 10000 {
			return nil, errors.New("目录超过 10000 个条目，请登记更小的目录")
		}
		for _, e := range entries {
			if e.IsDir() || !privacySourceMatches(r, e.Name()) {
				continue
			}
			if e.Type()&os.ModeSymlink != 0 {
				return nil, errors.New("匹配文件包含软链接，请单独登记其实际路径")
			}
			files = append(files, filepath.Join(r.ResolvedPath, e.Name()))
		}
		if err == io.EOF {
			break
		}
	}
	sort.Strings(files)
	return files, nil
}

func readPrivacyConfigSourceFiles(ctx context.Context, r *store.PrivacyConfigSourceRecord) ([]configimport.File, []string, string, error) {
	paths, err := privacySourceFiles(r)
	if err != nil {
		return nil, nil, "", err
	}
	files := make([]configimport.File, 0, len(paths))
	hash := sha256.New()
	total := 0
	for _, path := range paths {
		if err := ctx.Err(); err != nil {
			return nil, nil, "", err
		}
		content, err := readPrivacyConfigFile(path)
		if err != nil {
			return nil, nil, "", err
		}
		total += len(content)
		if total > configimport.MaxTotalBytes {
			return nil, nil, "", errors.New("来源文件合计超过 8 MiB")
		}
		name := filepath.Base(path)
		if r.Kind == "file" {
			name = filepath.Base(r.Path)
		}
		// 摘要同时包含实际路径和解析文件名；同一目标以不同格式登记时也需重解析。
		fmt.Fprintf(hash, "%d:%s:%d:%s:%d:", len(path), path, len(name), name, len(content))
		hash.Write(content)
		files = append(files, configimport.File{Name: name, Content: string(content)})
	}
	return files, paths, hex.EncodeToString(hash.Sum(nil)), nil
}

func readPrivacyConfigSource(ctx context.Context, r *store.PrivacyConfigSourceRecord) ([]store.PrivacyConfigSourceValue, string, error) {
	files, paths, digest, err := readPrivacyConfigSourceFiles(ctx, r)
	if err != nil {
		return nil, "", err
	}
	if digest == r.ContentHash {
		return nil, digest, nil
	}
	r.FileCount, r.ValueCount, r.ExcludedCount = len(files), 0, 0
	parsed, err := parsePrivacyConfigSource(ctx, files)
	if err != nil {
		return nil, "", err
	}
	r.ReviewHashes = configSourceReviewHashes(parsed)
	values := make([]store.PrivacyConfigSourceValue, 0)
	unique := make(map[string]bool)
	for i, result := range parsed {
		for _, finding := range result.Findings {
			candidate, record := configImportCandidate("", result.Name, finding, nil)
			if candidate.Status == "excluded" {
				r.ExcludedCount++
				continue
			}
			if !candidate.Selected {
				continue
			}
			record.SourceType = "config_source"
			record.SourceRef = fmt.Sprintf("source:%d", r.ID)
			record.Description = "配置来源自动同步；" + finding.Reason
			values = append(values, store.PrivacyConfigSourceValue{FilePath: paths[i], FieldPath: finding.Path, Secret: record})
			unique[record.ValueHash] = true
		}
	}
	r.ValueCount = len(unique)
	return values, digest, nil
}

// 来源同步与来源预览共享整批成功语义，不允许忽略损坏文件的部分结果。
func parsePrivacyConfigSource(ctx context.Context, files []configimport.File) ([]configimport.Result, error) {
	parsed, err := configimport.ParseSource(ctx, files)
	if err != nil {
		return nil, err
	}
	for i, result := range parsed {
		if result.Error != "" {
			return nil, fmt.Errorf("第 %d 个匹配文件解析失败：%s", i+1, result.Error)
		}
	}
	return parsed, nil
}

func configSourceReviewHashes(parsed []configimport.Result) []string {
	candidates, records := configImportCandidates(parsed, nil)
	hashes := make([]string, 0)
	for _, candidate := range candidates {
		if candidate.Status == "review" {
			hashes = append(hashes, records[candidate.ID].ValueHash)
		}
	}
	return hashes
}

func readPrivacyConfigFile(path string) ([]byte, error) {
	before, err := os.Lstat(path)
	if err != nil || !before.Mode().IsRegular() {
		return nil, errors.New("匹配文件不存在、不可读或不是普通文件")
	}
	if before.Size() > configimport.MaxFileBytes {
		return nil, errors.New("单个配置文件超过 2 MiB")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, errors.New("无法读取配置文件，请检查权限")
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !opened.Mode().IsRegular() || !os.SameFile(before, opened) {
		return nil, errors.New("配置文件正在替换，请稍后重试")
	}
	content, err := io.ReadAll(io.LimitReader(f, configimport.MaxFileBytes+1))
	if err != nil {
		return nil, errors.New("读取配置文件失败")
	}
	if len(content) > configimport.MaxFileBytes {
		return nil, errors.New("单个配置文件超过 2 MiB")
	}
	after, err := f.Stat()
	current, pathErr := os.Lstat(path)
	if err != nil || pathErr != nil || !os.SameFile(opened, current) || current.Mode()&os.ModeSymlink != 0 ||
		opened.Size() != after.Size() || !opened.ModTime().Equal(after.ModTime()) {
		return nil, errors.New("配置文件正在变化，请稍后重试")
	}
	return content, nil
}
