import { useEffect, useRef, useState } from 'react';
import { FileUp, CheckCircle2, AlertTriangle } from 'lucide-react';
import { Button, CustomSelect } from '@components/ui';
import Sheet from '@components/desktop/Sheet.jsx';
import { exactSecretCategoryLabel } from '../utils/privacyRules.js';
import { createConfigImportSession, defaultConfigSelection, readConfigImportFiles, selectedConfigIDs } from '../utils/configImport.js';

const statusLabels = { new: '建议加入', review: '按需选择', existing: '已存在', excluded: '未收录' };

const PrivacyConfigImportSheet = ({ onClose, onImported }) => {
  const pickerRef = useRef(null);
  const focusRef = useRef(null);
  const sessionRef = useRef(null);
  const [preview, setPreview] = useState(null);
  const [selected, setSelected] = useState(new Set());
  const [busy, setBusy] = useState('');
  const [error, setError] = useState('');
  const [result, setResult] = useState(null);
  const [filter, setFilter] = useState('all');
  const [page, setPage] = useState(0);

  useEffect(() => {
    const session = createConfigImportSession();
    sessionRef.current = session;
    return () => {
      sessionRef.current = null;
      session.dispose().catch(() => console.warn('配置导入预览释放失败；本机预览将在 10 分钟内过期'));
    };
  }, []);

  const chooseFiles = async (event) => {
    const files = Array.from(event.target.files || []);
    event.target.value = '';
    if (!files.length) return;
    setBusy('preview'); setError(''); setPreview(null); setSelected(new Set()); setFilter('all'); setPage(0);
    const session = sessionRef.current;
    try {
      const next = await session.preview(await readConfigImportFiles(files));
      if (sessionRef.current !== session || !next) return;
      setPreview(next); setSelected(defaultConfigSelection(next.candidates));
    } catch (err) {
      if (sessionRef.current === session) setError(err.message || String(err));
    } finally {
      if (sessionRef.current === session) setBusy('');
    }
  };

  const close = async () => {
    setBusy('close'); setError('');
    try { await sessionRef.current?.dispose(); onClose(); }
    catch { setError('临时预览释放失败，请重试关闭；预览最长保留 10 分钟'); setBusy(''); }
  };

  const confirm = async () => {
    if (!preview) return;
    setBusy('commit'); setError('');
    try {
      const saved = await sessionRef.current.commit(selectedConfigIDs(preview.candidates, selected));
      setResult(saved); setPreview(null); setSelected(new Set());
      await onImported();
    } catch (err) { setError(err.message || String(err)); }
    finally { setBusy(''); }
  };

  const candidates = preview?.candidates || [];
  const visible = filter === 'all' ? candidates : candidates.filter((c) => c.status === filter);
  const pageRows = visible.slice(page * 100, (page + 1) * 100);
  const selectedIDs = selectedConfigIDs(candidates, selected);
  const toggle = (id, checked) => setSelected((before) => {
    const next = new Set(before);
    if (checked) next.add(id); else next.delete(id);
    return next;
  });

  return (
    <Sheet title="从配置文件导入" label="从配置文件导入" width={880} onClose={close} closeDisabled={Boolean(busy)} initialFocusRef={focusRef}
      footerStart={error ? <span role="alert" className="text-danger">{error}</span> : <span className="text-fg-muted">{preview ? `已选 ${selectedIDs.length} 项 · 确认前不写入敏感值库` : '文件仅在本机解析'}</span>}
      footer={<><Button variant="ghost" onClick={close} disabled={Boolean(busy)}>{result ? '完成' : '取消'}</Button>{!result && <Button onClick={confirm} disabled={!selectedIDs.length || Boolean(busy)} loading={busy === 'commit'}>加入 {selectedIDs.length} 项并生效</Button>}</>}>
      {result ? (
        <div className="space-y-3 py-9 text-center" role="status">
          <CheckCircle2 size={30} className="mx-auto text-success" />
          <h3 className="text-base font-semibold text-fg">已加入 {result.added} 项敏感值</h3>
          <p className="text-sm text-fg-muted">{result.skipped > 0 ? `另有 ${result.skipped} 项已存在，原设置已保留。` : '敏感值库已更新。'}扫描行为遵循当前隐私保护模式。</p>
        </div>
      ) : <>
        <div className="my-4 flex items-center gap-4 rounded-lg border border-line bg-surface px-4 py-3">
          <FileUp size={27} className="shrink-0 text-fg-muted" aria-hidden="true" />
          <div className="min-w-0 flex-1">
            <p className="text-[13px] font-medium text-fg">选择配置，检查候选，一次加入</p>
            <p className="mt-1 text-xs leading-relaxed text-fg-muted">.env · JSON · YAML · properties，支持多选。单文件 2 MiB，合计 8 MiB。</p>
          </div>
          <Button ref={focusRef} variant="secondary" onClick={() => pickerRef.current?.click()} loading={busy === 'preview'} disabled={Boolean(busy)}>{preview ? '重新选择' : '选择文件'}</Button>
          <input ref={pickerRef} type="file" multiple aria-label="选择配置文件" className="hidden" onChange={chooseFiles} />
        </div>
        {!preview && !busy && <p className="py-7 text-center text-sm text-fg-muted">自动提取密码、凭据和完整私钥；账号与地址由你按需选择。</p>}
        {busy === 'preview' && <p className="py-7 text-center text-sm text-fg-muted" role="status">正在本机解析并去重…</p>}
        {preview && <>
          <div className="mb-3 space-y-1.5 text-xs">
            {preview.files.map((file, index) => <div key={index} className={`flex gap-2 ${file.error ? 'text-danger' : 'text-fg-muted'}`}>
              {file.error && <AlertTriangle size={13} className="shrink-0" />}
              <span className="break-all">{file.name} · {file.error || `已检查 ${file.scanned} 个值，发现 ${file.found} 项（跨文件相同值已合并）`}</span>
            </div>)}
          </div>
          <div role="group" aria-label="筛选导入候选" className="flex flex-wrap items-center gap-2 border-y border-line py-2">
            <CustomSelect
              size="sm"
              className="w-44"
              value={filter}
              onChange={(value) => { setFilter(value); setPage(0); }}
              options={[
                { value: 'all', label: `全部候选（${candidates.length}）` },
                ...Object.entries(statusLabels).map(([value, label]) => ({ value, label: `${label}（${candidates.filter((c) => c.status === value).length}）` }))
              ]}
            />
            <Button size="sm" variant="ghost" disabled={Boolean(busy)} onClick={() => setSelected(defaultConfigSelection(candidates))}>恢复建议选择</Button>
            <Button size="sm" variant="ghost" disabled={Boolean(busy)} onClick={() => setSelected(new Set())}>清除选择</Button>
          </div>
          <div className="mt-1 max-h-[38vh] overflow-auto">
            <table className="w-full table-fixed text-left text-xs">
              <thead className="sticky top-0 bg-pane text-fg-muted"><tr><th className="w-9 py-2"><span className="sr-only">选择</span></th><th className="w-[40%] py-2">来源与字段</th><th className="w-28 py-2">分类 / 值</th><th className="py-2">识别结果</th></tr></thead>
              <tbody>{pageRows.map((candidate) => <tr key={candidate.id} className="border-t border-line align-top">
                <td className="py-3"><input type="checkbox" className="app-checkbox" aria-label={`选择 ${candidate.name}`} checked={selected.has(candidate.id)} disabled={!candidate.selectable || Boolean(busy)} onChange={(e) => toggle(candidate.id, e.target.checked)} /></td>
                <td className="break-all py-3 pr-3 text-fg">{candidate.sources.map((source, index) => <div key={index} className={index ? 'mt-1 text-fg-muted' : ''}>{source}</div>)}</td>
                <td className="py-3 pr-2 text-fg-muted"><div>{exactSecretCategoryLabel(candidate.category)}</div><div className="mt-1 font-mono">{candidate.masked_value}</div>{candidate.value_length > 0 && <div className="mt-1">{candidate.value_length} 字节</div>}</td>
                <td className="py-3"><span className={`font-medium ${candidate.status === 'new' ? 'text-success' : 'text-fg-muted'}`}>{statusLabels[candidate.status]}</span><p className="mt-1 leading-relaxed text-fg-muted">{candidate.reason}</p></td>
              </tr>)}</tbody>
            </table>
            {visible.length === 0 && <p className="py-7 text-center text-fg-muted">{candidates.length ? '此分类没有候选' : '未找到可识别的候选，请检查文件结果。'}</p>}
          </div>
          {visible.length > 100 && <div className="mt-2 flex items-center justify-end gap-2 text-xs text-fg-muted">
            <span>第 {page + 1} / {Math.ceil(visible.length / 100)} 页</span>
            <Button size="sm" variant="ghost" disabled={page === 0} onClick={() => setPage(page - 1)}>上一页</Button>
            <Button size="sm" variant="ghost" disabled={(page + 1) * 100 >= visible.length} onClick={() => setPage(page + 1)}>下一页</Button>
          </div>}
        </>}
        <p className="mt-4 text-[11px] leading-relaxed text-fg-subtle">加入后用于所有适用请求；重复值保留原设置，旧密码继续保留。预览 10 分钟内有效。</p>
      </>}
    </Sheet>
  );
};

export default PrivacyConfigImportSheet;
