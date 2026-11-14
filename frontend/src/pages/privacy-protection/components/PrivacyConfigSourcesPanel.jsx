import { useRef, useState } from 'react';
import { FileKey2, Folder, Plus, RefreshCw, Pencil, Trash2 } from 'lucide-react';
import { Button, CustomSelect } from '@components/ui';
import Sheet from '@components/desktop/Sheet.jsx';
import Switch from '@components/desktop/Switch.jsx';
import ListEmpty from '@components/desktop/ListEmpty.jsx';
import { ToolbarButton } from '@components/layout/ToolbarButton.jsx';
import { FormFieldRow, FormSection } from '@components/desktop/FormSection.jsx';
import { useTimezone } from '@contexts/TimezoneContext.jsx';
import usePrivacyConfigSources from '../hooks/usePrivacyConfigSources.js';
import PrivacyConfigImportSheet from './PrivacyConfigImportSheet.jsx';
import { configSourceStatus, deleteConfigSource, saveConfigSource, syncConfigSources } from '../utils/configSources.js';

const KIND_OPTIONS = [{ value: 'file', label: '单个配置文件' }, { value: 'directory', label: '目录内的配置文件' }];

const SourceEditor = ({ source, busy, error, onSave, onClose }) => {
  const [form, setForm] = useState(source || { name: '', path: '', kind: 'file', patterns: '', enabled: true });
  const focus = useRef(null);
  const update = (key, value) => setForm((old) => ({ ...old, [key]: value }));
  const submit = async (event) => { event.preventDefault(); if (await onSave(form)) onClose(); };
  return (
    <Sheet title={source ? '编辑配置来源' : '添加配置来源'} width={600} initialFocusRef={focus} onClose={onClose}
      closeDisabled={busy} onSubmit={submit} footerStart={error ? <span role="alert" className="text-danger">{error}</span> : '文件内容仅在本机读取。'}
      footer={<><Button variant="secondary" onClick={onClose} disabled={busy}>取消</Button><Button type="submit" loading={busy}>{form.enabled ? '保存并自动同步' : '保存'}</Button></>}>
      <FormSection title="来源位置">
        <FormFieldRow label="类型" htmlFor="config-source-kind">
          <CustomSelect id="config-source-kind" value={form.kind} onChange={(value) => update('kind', value)} options={KIND_OPTIONS} />
        </FormFieldRow>
        <FormFieldRow label="本机路径" htmlFor="config-source-path" required hint="绝对路径或 ~/ 开头；目录不进入子目录。">
          <input ref={focus} id="config-source-path" required maxLength={4096} className="desk-field w-full font-mono" value={form.path} onChange={(event) => update('path', event.target.value)} placeholder={form.kind === 'file' ? '~/Projects/example/.env' : '~/Projects/example'} />
        </FormFieldRow>
        {form.kind === 'directory' && <FormFieldRow label="文件模式" htmlFor="config-source-patterns" hint="逗号分隔，留空使用默认模式。">
          <input id="config-source-patterns" maxLength={1024} className="desk-field w-full font-mono" value={form.patterns} onChange={(event) => update('patterns', event.target.value)} placeholder=".env,.env.*,*.yaml" />
        </FormFieldRow>}
        <FormFieldRow label="显示名称" htmlFor="config-source-name">
          <input id="config-source-name" className="desk-field w-full" value={form.name} onChange={(event) => update('name', event.target.value)} placeholder="可选，默认使用文件或目录名" />
        </FormFieldRow>
        <FormFieldRow label="自动同步"><Switch checked={form.enabled} onChange={(value) => update('enabled', value)} label="启用配置来源自动同步" /></FormFieldRow>
      </FormSection>
    </Sheet>
  );
};

const SyncRules = () => (
  <details className="shrink-0 border-b border-line-soft px-4 py-1.5 text-xs text-fg-muted">
    <summary className="w-fit cursor-pointer">同步规则</summary>
    <div className="mt-2 max-w-4xl space-y-1.5 leading-relaxed">
      <p>启动时检查一次，运行期间监听文件变化，不做定时扫描；未及时更新时可点「立即检查」。</p>
      <p>明确的密码与凭据自动加入；账号、地址、常见弱值和变量引用不自动加入，可点「需手动确认」按需选择。</p>
      <p>支持 .env、JSON、YAML、properties、AWS credentials、npmrc、netrc。单文件最多 2 MiB，每个来源合计 8 MiB；目录只读当前层级中匹配文件模式的文件。</p>
      <p>解析失败保留上次结果。暂停或移除来源保留已导入值，已停用词条保持停用，手动删除的词条不会被同一来源重新加入。</p>
    </div>
  </details>
);

const SourceRow = ({ source, busy, run, onEdit, onRemove, onReview }) => {
  const { formatListTimestamp } = useTimezone();
  const status = configSourceStatus(source);
  const Icon = source.kind === 'directory' ? Folder : FileKey2;
  const location = source.kind === 'directory' ? `${source.path}\n文件模式：${source.patterns}` : source.path;
  return (
    <tr className={source.enabled ? '' : 'text-fg-subtle'}>
      <td className="text-center"><span className="flex justify-center"><Switch label={`${source.name} 自动同步`} checked={source.enabled} disabled={busy} onChange={(enabled) => run(() => saveConfigSource({ ...source, enabled }))} /></span></td>
      <td className="py-1.5">
        <div className={`flex items-center gap-1.5 font-medium ${source.enabled ? 'text-fg' : ''}`}><Icon size={13} className="shrink-0 text-fg-muted" /><span className="truncate" title={source.name}>{source.name}</span></div>
        <div className="selectable truncate font-mono text-xs text-fg-subtle" title={location}>{source.path}</div>
      </td>
      <td className="py-1.5">
        <div className={status.tone}>{status.label}<span className="ml-2 text-xs tabular-nums text-fg-subtle">{source.last_scan_at ? formatListTimestamp(source.last_scan_at) : '尚未检查'}</span></div>
        {source.last_error && <div className="selectable whitespace-normal break-words text-xs leading-snug text-danger">{source.last_error}</div>}
      </td>
      <td className="py-1.5">
        <div className="text-fg-body">{source.last_success_at ? `${source.file_count} 个文件 · ${source.value_count} 项符合导入` : <span className="text-fg-subtle">—</span>}</div>
        {(source.review_count > 0 || source.excluded_count > 0) && <div className="text-xs text-fg-subtle">
          {source.review_count > 0 && <button type="button" className="rounded text-warn underline decoration-dotted underline-offset-2 hover:decoration-solid disabled:opacity-50" title="查看并选择此来源的候选" disabled={busy} onClick={() => onReview(source)}>{source.review_count} 项需手动确认</button>}
          {source.review_count > 0 && source.excluded_count > 0 && ' · '}
          {source.excluded_count > 0 && `${source.excluded_count} 项未收录`}
        </div>}
      </td>
      <td><div className="flex items-center justify-end gap-0.5">
        <ToolbarButton icon={RefreshCw} title={`立即检查 ${source.name}`} disabled={busy || !source.enabled} onClick={() => run(() => syncConfigSources(source.id))} />
        <ToolbarButton icon={Pencil} title={`编辑 ${source.name}`} disabled={busy} onClick={() => onEdit(source)} />
        <ToolbarButton icon={Trash2} title={`移除 ${source.name}`} disabled={busy} onClick={() => onRemove(source)} />
      </div></td>
    </tr>
  );
};

const SourceTable = ({ sources, ...rowProps }) => (
  <table className="desk-table min-w-[720px] table-fixed">
    <thead><tr>
      <th className="w-[52px] text-center">同步</th><th>来源</th><th className="w-[200px]">最近检查</th><th className="w-[220px]">识别结果</th><th className="w-[116px]" aria-label="操作" />
    </tr></thead>
    <tbody>{sources.map((source) => <SourceRow key={source.id} source={source} {...rowProps} />)}</tbody>
  </table>
);

const RemoveSheet = ({ source, busy, error, run, onClose }) => (
  <Sheet title="移除配置来源" width={420} onClose={() => !busy && onClose()} closeDisabled={busy}
    footerStart={error ? <span role="alert" className="text-danger">{error}</span> : ''}
    footer={<><Button variant="secondary" disabled={busy} onClick={onClose}>取消</Button><Button variant="danger" loading={busy} onClick={async () => { if (await run(() => deleteConfigSource(source.id))) onClose(); }}>移除来源</Button></>}>
    <p className="mt-5 text-sm text-fg">停止同步「{source.name}」</p><p className="mt-2 text-xs leading-relaxed text-fg-muted">本机文件和已导入的敏感值都会保留，之后可重新登记。</p>
  </Sheet>
);

const PrivacyConfigSourcesPanel = ({ onImported }) => {
  const { state, error, busy, reload, run } = usePrivacyConfigSources();
  const [editor, setEditor] = useState(null);
  const [removing, setRemoving] = useState(null);
  const [reviewSource, setReviewSource] = useState(null);
  const sources = state?.sources || [];
  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <div className="flex shrink-0 flex-wrap items-center gap-1.5 border-b border-line bg-inspector px-3 py-1.5">
        <Button size="sm" icon={Plus} disabled={busy} onClick={() => setEditor({ source: null })}>添加来源</Button>
        <Button size="sm" variant="secondary" icon={RefreshCw} title="立即检查全部已启用来源" disabled={busy || !sources.some((source) => source.enabled)} onClick={() => run(() => syncConfigSources())} loading={busy}>立即检查</Button>
        {state && <span className={`ml-auto text-xs tabular-nums ${state.watching ? 'text-fg-subtle' : 'text-warn'}`}>{sources.length} 个来源 · {state.watching ? '自动检查已开启' : '自动检查未启动'}</span>}
      </div>
      <SyncRules />
      {error && <div role="alert" className="flex shrink-0 items-center gap-3 border-b border-danger-line bg-danger-soft px-4 py-1.5 text-xs text-danger"><span className="selectable min-w-0 flex-1 break-all">{error}</span><Button size="sm" variant="ghost" onClick={reload}>重试读取</Button></div>}
      {state?.watch_error && <div role="alert" className="shrink-0 border-b border-warn-line bg-warn-soft px-4 py-1.5 text-xs text-warn">{state.watch_error}</div>}
      <div className="min-h-0 flex-1 overflow-auto">
        {!state ? <p role="status" className="py-12 text-center text-[13px] text-fg-subtle">{error ? '暂时无法读取配置来源，请重试。' : '正在读取配置来源…'}</p>
          : sources.length === 0 ? <ListEmpty icon={Folder} title="暂无配置来源" description="登记项目的 .env 或凭据文件后，启动和文件变化时会自动检查。" />
            : <SourceTable sources={sources} busy={busy} run={run} onEdit={(source) => setEditor({ source })} onRemove={setRemoving} onReview={setReviewSource} />}
      </div>
      {editor && <SourceEditor source={editor.source} busy={busy} error={error} onClose={() => setEditor(null)} onSave={(form) => run(() => saveConfigSource(form))} />}
      {reviewSource && <PrivacyConfigImportSheet source={reviewSource} onClose={() => setReviewSource(null)} onImported={async () => { await reload(); await onImported?.(); }} />}
      {removing && <RemoveSheet source={removing} busy={busy} error={error} run={run} onClose={() => setRemoving(null)} />}
    </div>
  );
};
export default PrivacyConfigSourcesPanel;
