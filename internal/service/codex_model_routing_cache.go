package service

import (
	"sinuo/internal/modelrouting"
	"time"
)

type codexNegativeKey struct {
	AccountID   int64
	Path, Model string
}

const codexNegativeCapacity = 1024

func (s *CodexModelRoutingService) negativeState(id int64, path, model string) (uint64, bool) {
	s.negativeMu.Lock()
	defer s.negativeMu.Unlock()
	key := codexNegativeKey{id, path, modelrouting.NormalizeModelName(model)}
	until, ok := s.negativeHits[key]
	if ok && !time.Now().Before(until) {
		delete(s.negativeHits, key)
		ok = false
	}
	return s.cacheGeneration[id], ok
}

// 配置编辑清缓存后，旧在途请求不得把失败重新写入。
func (s *CodexModelRoutingService) RecordNegativeHit(id int64, path, model string, generation uint64) {
	s.negativeMu.Lock()
	defer s.negativeMu.Unlock()
	if s.cacheGeneration[id] != generation {
		return
	}
	now := time.Now()
	for key, until := range s.negativeHits {
		if !now.Before(until) {
			delete(s.negativeHits, key)
		}
	}
	key := codexNegativeKey{id, path, modelrouting.NormalizeModelName(model)}
	if _, exists := s.negativeHits[key]; !exists && len(s.negativeHits) >= codexNegativeCapacity {
		var oldest codexNegativeKey
		var expiry time.Time
		for k, t := range s.negativeHits {
			if expiry.IsZero() || t.Before(expiry) {
				oldest = k
				expiry = t
			}
		}
		delete(s.negativeHits, oldest)
	}
	s.negativeHits[key] = now.Add(30 * time.Minute)
}
func (s *CodexModelRoutingService) ClearNegativeHits(id int64) {
	s.negativeMu.Lock()
	defer s.negativeMu.Unlock()
	s.cacheGeneration[id]++
	for key := range s.negativeHits {
		if key.AccountID == id {
			delete(s.negativeHits, key)
		}
	}
}
