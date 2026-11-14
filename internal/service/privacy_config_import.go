package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"time"

	"sinuo/internal/privacy"
	"sinuo/internal/privacy/configimport"
	"sinuo/internal/store"
)

const privacyConfigImportTTL = 10 * time.Minute

var errPrivacyConfigDictionaryLimit = errors.New("导入后的敏感值库原文超过 4 MiB，请减少本次选择")

// 对外只暴露固定原因；内部保留 cause，供诊断和 errors.Is 使用，避免解析/数据库错误携带原值。
type privacyConfigImportError struct{ cause error }

func (e *privacyConfigImportError) Error() string {
	return "批量导入失败，本次未写入或生效，请重试"
}
func (e *privacyConfigImportError) Unwrap() error { return e.cause }

type PrivacyConfigCandidate struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Category    string   `json:"category"`
	MaskedValue string   `json:"masked_value"`
	ValueLength int      `json:"value_length"`
	Sources     []string `json:"sources"`
	Reason      string   `json:"reason"`
	Status      string   `json:"status"`
	Selectable  bool     `json:"selectable"`
	Selected    bool     `json:"selected"`
}

type PrivacyConfigFileResult struct {
	Name    string `json:"name"`
	Format  string `json:"format"`
	Scanned int    `json:"scanned"`
	Found   int    `json:"found"`
	Error   string `json:"error"`
}

type PrivacyConfigPreview struct {
	ID         string                    `json:"id"`
	ExpiresAt  string                    `json:"expires_at"`
	Files      []PrivacyConfigFileResult `json:"files"`
	Candidates []PrivacyConfigCandidate  `json:"candidates"`
}

type PrivacyConfigImportResult struct {
	Added   int `json:"added"`
	Skipped int `json:"skipped"`
}

type privacyConfigImportSession struct {
	id           string
	expires      time.Time
	timer        *time.Timer
	records      map[string]*store.PrivacyExactSecretRecord
	sourceValues map[string][]store.PrivacyConfigSourceValue
	source       *store.PrivacyConfigSourceRecord
	sourceDigest string
}

// PreviewConfigImport 只在内存生成预览。原值不进入返回 DTO、日志或 SQLite。
func (s *PrivacyService) PreviewConfigImport(ctx context.Context, files []configimport.File) (PrivacyConfigPreview, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.clearConfigImportLocked()
	var out PrivacyConfigPreview
	if s.store == nil || s.SnapshotVersion() == 0 {
		return out, errors.New("隐私服务尚未就绪")
	}
	parsed, err := configimport.Parse(ctx, files)
	if err != nil {
		return out, err
	}
	return s.previewParsedConfigImportLocked(ctx, parsed)
}

// 两种入口共用去重、分类、已有值识别与临时预览生命周期。
func (s *PrivacyService) previewParsedConfigImportLocked(ctx context.Context, parsed []configimport.Result) (PrivacyConfigPreview, error) {
	var out PrivacyConfigPreview
	existing, err := s.store.ListExactSecrets(ctx)
	if err != nil {
		return out, errors.New("读取本地敏感值失败，请重试")
	}
	hashes := make(map[string]*store.PrivacyExactSecretRecord, len(existing))
	for _, r := range existing {
		hashes[r.ValueHash] = r
	}
	var token [24]byte
	if _, err := rand.Read(token[:]); err != nil {
		return out, errors.New("无法创建导入预览")
	}
	state := &privacyConfigImportSession{id: hex.EncodeToString(token[:]), expires: time.Now().Add(privacyConfigImportTTL), records: make(map[string]*store.PrivacyExactSecretRecord)}
	out.ID, out.ExpiresAt = state.id, state.expires.Format(time.RFC3339)
	out.Files = make([]PrivacyConfigFileResult, 0, len(parsed))
	for _, file := range parsed {
		out.Files = append(out.Files, PrivacyConfigFileResult{Name: file.Name, Format: file.Format, Scanned: file.Scanned, Found: len(file.Findings), Error: file.Error})
	}
	out.Candidates, state.records = configImportCandidates(parsed, hashes)
	state.timer = time.AfterFunc(privacyConfigImportTTL, func() { s.CancelConfigImport(state.id) })
	s.configImport = state
	return out, nil
}

// 分类与同值合并只有这一套规则，来源待确认计数和预览使用相同候选。
func configImportCandidates(parsed []configimport.Result, hashes map[string]*store.PrivacyExactSecretRecord) ([]PrivacyConfigCandidate, map[string]*store.PrivacyExactSecretRecord) {
	candidates := make([]PrivacyConfigCandidate, 0)
	records := make(map[string]*store.PrivacyExactSecretRecord)
	indexes := make(map[string]int)
	for _, file := range parsed {
		for _, f := range file.Findings {
			source := file.Name + ":" + f.Path
			if !f.Excluded {
				hash := HashPrivacySecretValue(f.Value)
				if index, ok := indexes[hash]; ok {
					c := &candidates[index]
					if len(c.Sources) < 32 {
						c.Sources = append(c.Sources, source)
					}
					// 同值同时来自地址和凭据字段时，采用更严格的凭据分类准入。
					if exactSecretMinLength(f.Category) > exactSecretMinLength(c.Category) || (c.Status == "review" && !f.Optional) {
						updated, record := configImportCandidate(c.ID, file.Name, f, hashes[hash])
						updated.Sources = c.Sources
						*c = updated
						delete(records, c.ID)
						if record != nil {
							records[c.ID] = record
						}
					}
					continue
				}
				indexes[hash] = len(candidates)
			}
			c, record := configImportCandidate(strconv.Itoa(len(candidates)+1), file.Name, f, hashes[HashPrivacySecretValue(f.Value)])
			candidates = append(candidates, c)
			if record != nil {
				records[c.ID] = record
			}
		}
	}
	return candidates, records
}

func configImportCandidate(id, file string, f configimport.Finding, existing *store.PrivacyExactSecretRecord) (PrivacyConfigCandidate, *store.PrivacyExactSecretRecord) {
	name := file + ":" + f.Path
	c := PrivacyConfigCandidate{ID: id, Name: name, Category: f.Category, MaskedValue: "••••••••", ValueLength: len(f.Value), Sources: []string{name}, Reason: f.Reason, Status: "new"}
	if f.Excluded {
		c.Status = "excluded"
		c.MaskedValue = "—"
		return c, nil
	}
	if existing != nil {
		c.Status = "existing"
		c.Reason = "已存在，保留原名称、分类与启用状态"
		if !existing.Enabled {
			c.Reason += "（当前停用）"
		}
		return c, nil
	}
	r, err := normalizeExactSecretRecord(&store.PrivacyExactSecretRecord{Enabled: true, Name: name, SecretValue: f.Value, Category: f.Category, SourceType: "imported", SourceRef: name, Description: "配置文件导入；" + f.Reason}, true)
	if err != nil {
		c.Status = "excluded"
		c.Reason = fmt.Sprintf("长度不足 %d 字节，容易误匹配普通文本，本项暂不收录", exactSecretMinLength(f.Category))
		return c, nil
	}
	c.Selectable = true
	c.Selected = !f.Optional
	if f.Optional {
		c.Status = "review"
	}
	return c, r
}

// CommitConfigImport 只接受预览 ID 与用户勾选的候选 ID，不接受客户端回传原值。
func (s *PrivacyService) CommitConfigImport(ctx context.Context, id string, selected []string) (PrivacyConfigImportResult, error) {
	// 与来源同步、登记修改保持相同锁顺序，确认时来源边界不能被并发替换。
	s.sourceMu.Lock()
	defer s.sourceMu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	var out PrivacyConfigImportResult
	state := s.configImport
	if state == nil || state.id != id {
		return out, errors.New("导入预览已失效，请重新生成预览")
	}
	if !time.Now().Before(state.expires) {
		s.clearConfigImportLocked()
		return out, errors.New("导入预览已过期，请重新生成预览")
	}
	if len(selected) == 0 || len(selected) > configimport.MaxValues {
		return out, errors.New("请选择有效的导入候选")
	}
	seen := make(map[string]bool, len(selected))
	records := make([]*store.PrivacyExactSecretRecord, 0, len(selected))
	var sourceValues []store.PrivacyConfigSourceValue
	for _, key := range selected {
		r := state.records[key]
		if r == nil {
			return out, errors.New("包含不可导入的候选，请重新检查选择")
		}
		if !seen[key] {
			records = append(records, r)
			if state.source != nil {
				values := state.sourceValues[key]
				if len(values) == 0 {
					return out, errors.New("来源候选关联已失效，请重新生成预览")
				}
				sourceValues = append(sourceValues, values...)
			}
			seen[key] = true
		}
	}
	if err := s.validateConfigSourceReview(ctx, state); err != nil {
		return out, err
	}
	var next *privacy.Snapshot
	prepare := func(all []*store.PrivacyExactSecretRecord) error {
		var err error
		next, err = s.snapshotWithExactSecretsLocked(all)
		return err
	}
	var added int
	var err error
	if state.source == nil {
		added, err = s.store.ImportExactSecrets(ctx, records, prepare)
	} else {
		added, err = s.store.ImportConfigSourceSecrets(ctx, state.source.ID, sourceValues, prepare)
	}
	if err != nil {
		if errors.Is(err, errPrivacyConfigDictionaryLimit) {
			return out, errPrivacyConfigDictionaryLimit
		}
		if errors.Is(err, privacy.ErrLiteralBudget) {
			return out, privacy.ErrLiteralBudget
		}
		if errors.Is(err, store.ErrConfigSourceHistoryLimit) {
			return out, store.ErrConfigSourceHistoryLimit
		}
		return out, &privacyConfigImportError{cause: err}
	}
	if next != nil {
		s.publishSnapshotLocked(next)
	}
	out.Added, out.Skipped = added, len(records)-added
	s.clearConfigImportLocked()
	return out, nil
}

// CancelConfigImport 取消只释放对应预览，旧窗口不能取消后来生成的新预览。
func (s *PrivacyService) CancelConfigImport(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.configImport != nil && s.configImport.id == id {
		s.clearConfigImportLocked()
	}
}

func (s *PrivacyService) clearConfigImportLocked() {
	if s.configImport == nil {
		return
	}
	if s.configImport.timer != nil {
		s.configImport.timer.Stop()
	}
	s.configImport.records = nil
	s.configImport.sourceValues = nil
	s.configImport = nil
}

func (s *PrivacyService) snapshotWithExactSecretsLocked(records []*store.PrivacyExactSecretRecord) (*privacy.Snapshot, error) {
	current := s.CurrentSnapshot()
	if current == nil || current.Version == 0 {
		return nil, errors.New("隐私服务尚未就绪")
	}
	total := 0
	for _, r := range records {
		total += len(r.SecretValue)
	}
	if total > 4<<20 {
		return nil, errPrivacyConfigDictionaryLimit
	}
	compiled, err := privacy.CompileRules(rulesWithExactSecrets(current, records))
	if err != nil {
		return nil, err
	}
	next := *current
	next.Rules = compiled
	if next.CompileBlocked {
		next.CompileBlocked = false
		next.CompileError = ""
	}
	next.Cache = privacy.NewSegmentCache(privacy.DefaultSegmentCacheCapacity)
	return &next, nil
}
