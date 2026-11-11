import { useEffect, useRef, useState } from 'react';
import { BookOpen, ChevronLeft, ChevronRight, RefreshCw, Trash2 } from 'lucide-react';
import ListEmpty from '@components/desktop/ListEmpty.jsx';
import { ToolbarButton } from '@components/layout/ToolbarButton.jsx';
import { useTimezone } from '@contexts/TimezoneContext.jsx';
import useConfirm from '@hooks/useConfirm.js';
import { resolveErrorMessage } from '@utils/errors.js';
import { clearPrivacyRestoreDictionary, listPrivacyRestoreDictionary, revealPrivacyRestoreValue } from '@utils/wailsApi.js';

const PAGE_SIZE = 50;
const REFRESH_MS = 3000;

// 原值只驻留在当前展开行。翻页、收起、条目更换或离开页签会卸载并丢弃迟到结果。
const RevealedValue = ({ token, entryID }) => {
  const [value, setValue] = useState(null);
  const [error, setError] = useState('');
  useEffect(() => {
    let active = true;
    revealPrivacyRestoreValue(token, entryID).then(
      (raw) => { if (active) setValue(raw); },
      (err) => { if (active) setError(resolveErrorMessage(err, '原值读取失败')); }
    );
    return () => { active = false; };
  }, [token, entryID]);
  if (error) return <div role="alert" className="whitespace-normal text-xs text-danger">{error}</div>;
  return <div className="selectable max-h-32 overflow-auto whitespace-pre-wrap break-all py-1 font-mono text-xs">{value ?? '读取中…'}</div>;
};

const DictionaryPage = ({ offset, setOffset, onClear, clearing, restoreUnavailableInMode }) => {
  const { formatTimestamp } = useTimezone();
  const [page, setPage] = useState(null);
  const [error, setError] = useState('');
  const [selectedID, setSelectedID] = useState(null);
  const refresh = useRef(null);

  useEffect(() => {
    let active = true;
    let loading = false;
    let timer;
    // 定时、手动及重新可见时共用一次在途读取。
    const readPage = async () => {
      clearTimeout(timer);
      if (loading || document.hidden || !active) return;
      loading = true;
      try {
        const result = await listPrivacyRestoreDictionary(offset, PAGE_SIZE);
        if (!active) return;
        if (offset > 0 && offset >= result.total) {
          setOffset(Math.floor(Math.max(0, result.total - 1) / PAGE_SIZE) * PAGE_SIZE);
          return;
        }
        const entries = result.entries ?? [];
        setPage({ ...result, entries });
        setSelectedID((id) => entries.some((entry) => entry.entry_id === id) ? id : null);
        setError('');
      } catch (err) {
        if (!active) return;
        setSelectedID(null);
        setPage(null);
        setError(resolveErrorMessage(err, '字典读取失败'));
      } finally {
        loading = false;
        if (active && !document.hidden) timer = setTimeout(readPage, REFRESH_MS);
      }
    };
    const onVisibility = () => {
      if (document.hidden) {
        clearTimeout(timer);
        setSelectedID(null);
      } else {
        void readPage();
      }
    };
    refresh.current = readPage;
    void readPage();
    document.addEventListener('visibilitychange', onVisibility);
    return () => {
      active = false;
      clearTimeout(timer);
      refresh.current = null;
      document.removeEventListener('visibilitychange', onVisibility);
    };
  }, [offset, setOffset]);

  const inactiveDescription = restoreUnavailableInMode ? '当前模式不支持回填。切换到「脱敏转发」后生效。' : '响应回填未开启。';
  return (
    <>
      <div className="flex shrink-0 flex-wrap items-center gap-x-4 gap-y-0.5 border-b border-line-soft px-4 py-1.5">
        {page && <span className="text-[13px] font-medium text-fg">当前 <span className="tabular-nums">{page.total}</span> 条映射</span>}
        <span className="text-xs text-fg-muted">仅保留在本次运行中，关闭回填或重启 Sinuo 后清空。</span>
        <ToolbarButton className="ml-auto" icon={RefreshCw} title="刷新内存回填字典" onClick={() => refresh.current?.()} />
        <ToolbarButton icon={Trash2} label="清空字典" disabled={!page?.total} loading={clearing} onClick={onClear} />
      </div>
      <details className="shrink-0 border-b border-line-soft px-4 py-1.5 text-xs text-fg-muted">
        <summary className="w-fit cursor-pointer">清空后会怎样</summary>
        <div className="mt-2 max-w-4xl space-y-1.5 leading-relaxed">
          <p>此前只靠字典遮蔽的值会明文发出，例如从 password=… 中识别、之后单独出现的密码。</p>
          <p>响应里的旧占位符原样保留，直到后续请求中规则再次识别出对应的值；未还原次数见请求详情。</p>
        </div>
      </details>
      <div className="min-h-0 flex-1 overflow-auto">
        {error ? <div role="alert" className="px-4 py-3 text-sm text-danger">{error}</div>
          : !page ? <ListEmpty title="正在读取内存回填字典…" />
          : !page.total ? <ListEmpty icon={BookOpen} title="当前内存中暂无映射" description={page.enabled ? '下一次请求生成的占位符会显示在这里。' : inactiveDescription} />
          : (
            <table className="desk-table">
              <thead><tr><th>占位符</th><th>对应隐私值</th><th>实际回填</th><th>最近回填</th></tr></thead>
              <tbody>
                {page.entries.map((entry) => (
                  <tr key={entry.entry_id}>
                    <td className="min-w-44 max-w-xs"><span className="selectable block whitespace-normal break-all py-1.5 font-mono text-xs">{entry.token}</span></td>
                    <td className="min-w-36 max-w-sm">
                      <div className="flex flex-wrap items-baseline gap-x-2">
                        <span className="font-mono text-xs text-fg-muted">{entry.masked_value}</span>
                        <button type="button" className="text-xs text-accent hover:underline" aria-expanded={selectedID === entry.entry_id}
                          onClick={() => setSelectedID(selectedID === entry.entry_id ? null : entry.entry_id)}>
                          {selectedID === entry.entry_id ? '收起原值' : '查看原值'}
                        </button>
                      </div>
                      {selectedID === entry.entry_id && <RevealedValue token={entry.token} entryID={entry.entry_id} />}
                    </td>
                    <td className="text-xs text-fg-body">{entry.text_count || entry.tool_count
                      ? `正文 ${entry.text_count} 次 · 工具参数 ${entry.tool_count} 次` : '尚未回填'}</td>
                    <td className="text-xs tabular-nums text-fg-muted">{entry.last_restored_at ? formatTimestamp(entry.last_restored_at) : '—'}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}
      </div>
      {page?.total > PAGE_SIZE && (
        <nav aria-label="字典分页" className="flex shrink-0 items-center justify-end gap-2 border-t border-line-soft px-4 py-1 text-xs text-fg-muted">
          <span className="tabular-nums">{offset + 1}–{Math.min(offset + PAGE_SIZE, page.total)} / {page.total}</span>
          <ToolbarButton title="上一页" icon={ChevronLeft} disabled={!offset} onClick={() => setOffset(offset - PAGE_SIZE)} />
          <ToolbarButton title="下一页" icon={ChevronRight} disabled={offset + PAGE_SIZE >= page.total} onClick={() => setOffset(offset + PAGE_SIZE)} />
        </nav>
      )}
    </>
  );
};

export default function PrivacyRestoreDictionaryPanel({ restoreUnavailableInMode }) {
  const [offset, setOffset] = useState(0);
  const [generation, setGeneration] = useState(0);
  const [clearing, setClearing] = useState(false);
  const [clearError, setClearError] = useState('');
  const { confirm, confirmDialog } = useConfirm();
  const clear = async () => {
    if (!(await confirm({
      title: '清空内存回填字典',
      message: '已发出的请求不受影响。清空后，此前只靠字典遮蔽的值会明文发出，旧占位符要等规则再次识别出原值才会换回。',
      confirmText: '清空字典',
      danger: true
    }))) return;
    setClearing(true);
    setClearError('');
    try {
      await clearPrivacyRestoreDictionary();
      setOffset(0);
      // 卸载旧页的轮询和已展开的原值，迟到的读取不能恢复旧映射。
      setGeneration((value) => value + 1);
    } catch (err) {
      setClearError(resolveErrorMessage(err, '清空字典失败'));
    } finally {
      setClearing(false);
    }
  };
  return (
    <>
      {clearError && <div role="alert" className="px-4 py-2 text-xs text-danger">{clearError}</div>}
      <DictionaryPage key={`${generation}-${offset}`} offset={offset} setOffset={setOffset} onClear={clear} clearing={clearing} restoreUnavailableInMode={restoreUnavailableInMode} />
      {confirmDialog}
    </>
  );
}
