package service

import (
	"context"
	"errors"

	"sinuo/internal/store"
)

// PreviewConfigSourceImport 只按已登记的来源 ID 读取；复用扫描边界和导入预览。
// 原值仅留在临时会话中，持久化的待确认关联只有值摘要。
func (s *PrivacyService) PreviewConfigSourceImport(ctx context.Context, sourceID int64) (PrivacyConfigPreview, error) {
	s.sourceMu.Lock()
	defer s.sourceMu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.clearConfigImportLocked()
	var out PrivacyConfigPreview
	if s.store == nil || s.SnapshotVersion() == 0 {
		return out, errors.New("隐私服务尚未就绪")
	}
	source, err := s.configSourceForReview(ctx, sourceID)
	if err != nil {
		return out, err
	}
	files, paths, digest, err := readPrivacyConfigSourceFiles(ctx, source)
	if err != nil {
		return out, err
	}
	parsed, err := parsePrivacyConfigSource(ctx, files)
	if err != nil {
		return out, err
	}
	out, err = s.previewParsedConfigImportLocked(ctx, parsed)
	if err != nil {
		return out, err
	}
	if err := s.store.RefreshConfigSourceReviews(ctx, sourceID, configSourceReviewHashes(parsed)); err != nil {
		s.clearConfigImportLocked()
		return PrivacyConfigPreview{}, errors.New("更新待确认数量失败，请重新检查")
	}
	s.configImport.source = source
	s.configImport.sourceDigest = digest
	// 按最终去重后的候选 ID 保留所有实际文件和字段位置，仅在确认时写入历史。
	idsByHash := make(map[string]string, len(s.configImport.records))
	for id, record := range s.configImport.records {
		idsByHash[record.ValueHash] = id
	}
	s.configImport.sourceValues = make(map[string][]store.PrivacyConfigSourceValue)
	for i, file := range parsed {
		for _, finding := range file.Findings {
			if finding.Excluded {
				continue
			}
			if id, ok := idsByHash[HashPrivacySecretValue(finding.Value)]; ok {
				s.configImport.sourceValues[id] = append(s.configImport.sourceValues[id], store.PrivacyConfigSourceValue{
					FilePath: paths[i], FieldPath: finding.Path, Secret: s.configImport.records[id],
				})
			}
		}
	}
	return out, nil
}

func (s *PrivacyService) configSourceForReview(ctx context.Context, id int64) (*store.PrivacyConfigSourceRecord, error) {
	if id <= 0 {
		return nil, errors.New("配置来源 ID 无效")
	}
	sources, err := s.store.ListConfigSources(ctx)
	if err != nil {
		return nil, errors.New("读取配置来源失败，请重试")
	}
	for _, source := range sources {
		if source.ID == id {
			return source, nil
		}
	}
	return nil, errors.New("配置来源已不存在，请关闭预览并刷新")
}

// 确认前再次校验来源定义及内容，防止用户看到的候选与实际登记范围不一致。
// 调用方依次持有 sourceMu 和 mu，与后台同步保持相同锁顺序。
func (s *PrivacyService) validateConfigSourceReview(ctx context.Context, session *privacyConfigImportSession) error {
	if session.source == nil {
		return nil
	}
	current, err := s.configSourceForReview(ctx, session.source.ID)
	if err != nil {
		return err
	}
	before := session.source
	if current.Path != before.Path || current.ResolvedPath != before.ResolvedPath || current.Kind != before.Kind || current.Patterns != before.Patterns {
		return errors.New("配置来源已修改，请重新检查后确认")
	}
	_, _, digest, err := readPrivacyConfigSourceFiles(ctx, current)
	if err != nil {
		return err
	}
	if digest != session.sourceDigest {
		return errors.New("来源文件已变化，请重新检查后确认")
	}
	return nil
}
