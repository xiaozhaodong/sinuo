package service

import (
	"bytes"
	"errors"
	"time"

	"sinuo/internal/privacy"
)

// restoreDictionaryForSnapshot 使普通规则热更新保留字典；关停或换密钥后在途响应仍持有旧代际。
func (s *PrivacyService) restoreDictionaryForSnapshot(previous, next *privacy.Snapshot) *privacy.RestoreDict {
	enabled := next.Settings.Mode == privacy.ModeRedact && next.Settings.ResponseRestoreEnabled
	if previous != nil && previous.Dict != nil {
		if enabled && bytes.Equal(previous.RestoreKey, next.RestoreKey) {
			return previous.Dict
		}
		previous.Dict.Retire()
	}
	if enabled {
		return s.restoreCapacity.NewDictionary()
	}
	return nil
}

func (s *PrivacyService) ListRestoreDictionary(offset, limit int) (privacy.RestoreDictionaryPage, error) {
	if s == nil || s.SnapshotVersion() == 0 {
		return privacy.RestoreDictionaryPage{}, errors.New("隐私服务尚未就绪")
	}
	return s.CurrentSnapshot().Dict.Page(offset, limit, func(value string) string {
		return MaskPrivacySecretValue(value, "")
	})
}

func (s *PrivacyService) RevealRestoreValue(token, entryID string) (string, error) {
	if s == nil || s.SnapshotVersion() == 0 {
		return "", errors.New("隐私服务尚未就绪")
	}
	return s.CurrentSnapshot().Dict.Reveal(token, entryID)
}

// ClearRestoreDictionary 切换字典代际。已发布的响应继续使用旧映射，尚未发布的旧草稿会被拒绝，需由客户端重发请求。
func (s *PrivacyService) ClearRestoreDictionary() error {
	if s == nil {
		return errors.New("隐私服务尚未就绪")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	current := s.CurrentSnapshot()
	if current == nil || current.Version == 0 {
		return errors.New("隐私服务尚未就绪")
	}
	if current.Dict == nil {
		return nil
	}
	next := *current
	next.Dict = s.restoreCapacity.NewDictionary()
	next.Version = s.version.Add(1)
	next.LoadedAt = time.Now()
	current.Dict.Retire()
	s.snapshot.Store(&next)
	return nil
}
