package main

import "sinuo/internal/privacy"

// ListPrivacyRestoreDictionary 分页读取当前进程的占位符、掩码和实际交付次数。
func (a *App) ListPrivacyRestoreDictionary(offset, limit int) (privacy.RestoreDictionaryPage, error) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	svc, err := a.privacyServiceOrError()
	if err != nil {
		return privacy.RestoreDictionaryPage{}, err
	}
	return svc.ListRestoreDictionary(offset, limit)
}

// RevealPrivacyRestoreValue 按占位符和本次驻留标识读取单项原值。
func (a *App) RevealPrivacyRestoreValue(token, entryID string) (string, error) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	svc, err := a.privacyServiceOrError()
	if err != nil {
		return "", err
	}
	return svc.RevealRestoreValue(token, entryID)
}

// ClearPrivacyRestoreDictionary 清空后续请求使用的内存回填字典。
func (a *App) ClearPrivacyRestoreDictionary() error {
	a.mu.RLock()
	defer a.mu.RUnlock()
	svc, err := a.privacyServiceOrError()
	if err != nil {
		return err
	}
	return svc.ClearRestoreDictionary()
}
