// Claude Code 入口状态：接入卡片与请求页切换栏共用。
// 保存后后端推送 claude-code-entry:update，另一处打开的视图随之刷新。

import { useCallback, useEffect, useState } from 'react';
import { resolveErrorMessage } from '@utils/errors.js';
import {
  getClaudeCodeEntryStatus,
  isWailsEnvironment,
  normalizeClaudeCodeEntryInfo,
  saveClaudeCodeEntrySettings,
  setClaudeCodeEntryMode,
  subscribeToEvent
} from '@utils/wailsApi.js';

export const CLAUDE_CODE_ENTRY_UPDATE_EVENT = 'claude-code-entry:update';

const useClaudeCodeEntry = () => {
  const available = isWailsEnvironment();
  const [info, setInfo] = useState(null);
  const [loading, setLoading] = useState(available);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');

  const refresh = useCallback(async () => {
    if (!available) return;
    setLoading(true);
    setError('');
    try {
      setInfo(await getClaudeCodeEntryStatus());
    } catch (err) {
      setError(resolveErrorMessage(err, '读取 Claude Code 入口状态失败'));
    } finally {
      setLoading(false);
    }
  }, [available]);

  useEffect(() => {
    if (!available) return undefined;
    refresh();
    return subscribeToEvent(CLAUDE_CODE_ENTRY_UPDATE_EVENT, (next) => {
      if (next && typeof next === 'object') setInfo(normalizeClaudeCodeEntryInfo(next));
    });
  }, [available, refresh]);

  // 写操作统一处理忙碌态与错误；成功返回最新状态，失败返回 null 并留下错误文案
  const run = useCallback(async (operation, fallbackMessage) => {
    setBusy(true);
    setError('');
    try {
      const next = await operation();
      setInfo(next);
      return next;
    } catch (err) {
      setError(resolveErrorMessage(err, fallbackMessage));
      return null;
    } finally {
      setBusy(false);
    }
  }, []);

  const save = useCallback((patch) => run(
    () => saveClaudeCodeEntrySettings({ ...info, ...patch }),
    '保存 Claude Code 入口配置失败'
  ), [info, run]);

  const setMode = useCallback((mode) => run(
    () => setClaudeCodeEntryMode(mode),
    '切换 Claude Code 接入模式失败'
  ), [run]);

  return { available, info, loading, busy, error, refresh, save, setMode };
};

export default useClaudeCodeEntry;
