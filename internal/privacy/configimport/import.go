// Package configimport 仅在用户导入配置时提取候选；不执行表达式，也不读取文件或环境变量。
package configimport

import (
	"context"
	"errors"
	"path"
	"strings"
	"unicode/utf8"
)

const (
	MaxFiles      = 32 // 手动导入单批文件数，自动来源扫描不使用此限制。
	MaxFileBytes  = 2 << 20
	MaxTotalBytes = 8 << 20
	MaxValues     = 10000
	MaxValueBytes = 64 << 10
	maxDepth      = 64
	maxPathBytes  = 512
)

// File 的内容只用于本次解析，不应记录到日志或持久化。
type File struct {
	Name    string
	Content string
}

// Finding 同时表示可登记值和需要解释的未收录项。Value 不允许序列化。
type Finding struct {
	Path     string
	Category string
	Reason   string
	Value    string `json:"-"`
	Optional bool
	Excluded bool
}

type Result struct {
	Name     string
	Format   string
	Findings []Finding
	Scanned  int
	Error    string
}

type parser struct {
	ctx    context.Context
	result *Result
	nodes  int
}

func (p *parser) step(depth int) error {
	if err := p.ctx.Err(); err != nil {
		return err
	}
	p.nodes++
	if depth > maxDepth || p.nodes > 100000 {
		return errors.New("配置层数或字段数超过导入上限")
	}
	return nil
}

func (p *parser) addFinding(f Finding) error {
	if len(p.result.Findings) >= MaxValues {
		return errors.New("配置候选数超过导入上限")
	}
	f.Path = boundedFieldPath(f.Path)
	p.result.Findings = append(p.result.Findings, f)
	return nil
}

// Parse 校验手动导入批次，再交给共用解析器。
func Parse(ctx context.Context, files []File) ([]Result, error) {
	if len(files) == 0 || len(files) > MaxFiles {
		return nil, errors.New("请选择 1–32 个配置文件")
	}
	return ParseSource(ctx, files)
}

// ParseSource 按文件类型解析整个来源，不限制文件数，统一检查大小与候选容量。
// 一个文件解析失败时丢弃该文件的部分结果，手动导入仍可预览其他文件。
func ParseSource(ctx context.Context, files []File) ([]Result, error) {
	total := 0
	for _, f := range files {
		if len(f.Content) > MaxFileBytes {
			return nil, errors.New("单个配置文件不能超过 2 MiB")
		}
		total += len(f.Content)
	}
	if total > MaxTotalBytes {
		return nil, errors.New("配置文件合计不能超过 8 MiB")
	}
	results := make([]Result, 0, len(files))
	count := 0
	for _, f := range files {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		r := Result{Name: SafeName(f.Name), Format: fileFormat(f.Name)}
		p := parser{ctx: ctx, result: &r}
		content := strings.TrimPrefix(f.Content, "\ufeff")
		var err error
		switch {
		case !utf8.ValidString(content) || strings.ContainsRune(content, 0):
			err = errors.New("文件必须是无 NUL 字符的 UTF-8 文本")
		case r.Format == "json":
			err = p.parseJSON(content)
		case r.Format == "yaml":
			err = p.parseYAML(content)
		case r.Format == "env":
			err = p.parseEnv(content)
		case r.Format == "aws" || r.Format == "npmrc":
			err = p.parseCredentialAssignments(content, r.Format)
		case r.Format == "netrc":
			err = p.parseNetrc(content)
		case r.Format == "properties":
			err = p.parseProperties(content)
		default:
			err = errors.New("仅支持 .env、JSON、YAML/YML、properties、AWS credentials、npmrc 和 netrc 文件")
		}
		if err != nil {
			r.Error = err.Error()
			r.Findings = nil
			r.Scanned = 0
		}
		count += len(r.Findings)
		if count > MaxValues {
			return nil, errors.New("本次候选超过 10000 条，请分批导入")
		}
		results = append(results, r)
	}
	return results, nil
}

func fileFormat(name string) string {
	name = strings.ToLower(SafeName(name))
	switch name {
	case "credentials":
		return "aws"
	case ".npmrc", "npmrc":
		return "npmrc"
	case ".netrc", "_netrc":
		return "netrc"
	}
	switch path.Ext(name) {
	case ".json":
		return "json"
	case ".yaml", ".yml":
		return "yaml"
	case ".properties":
		return "properties"
	case ".env":
		return "env"
	}
	if strings.HasPrefix(name, ".env.") {
		return "env"
	}
	return ""
}

// SupportsFileName 供登记来源的目录枚举与手动导入共用格式准入。
func SupportsFileName(name string) bool { return fileFormat(name) != "" }

// SafeName 只保留文件别名，不接受绝对路径、控制字符或过长元数据。
func SafeName(name string) string {
	name = path.Base(strings.ReplaceAll(name, "\\", "/"))
	runes := []rune(strings.Map(func(r rune) rune {
		if r < 32 || r == 127 {
			return -1
		}
		return r
	}, name))
	if len(runes) > 120 {
		runes = runes[:120]
	}
	return string(runes)
}

func fieldPath(parent, key string) string {
	if len(key) > 80 || strings.ContainsAny(key, "\n\r\t") {
		key = "[复杂字段]"
	}
	if parent == "" {
		return key
	}
	return boundedFieldPath(parent + "." + key)
}

// 来源只是定位提示；截断使用可见省略号，保持 UTF-8 完整且不继续增长。
func boundedFieldPath(location string) string {
	if len(location) <= maxPathBytes {
		return location
	}
	n := maxPathBytes - len("…")
	for n > 0 && !utf8.RuneStart(location[n]) {
		n--
	}
	return location[:n] + "…"
}
