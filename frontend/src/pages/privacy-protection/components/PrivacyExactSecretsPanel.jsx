import { useMemo, useRef, useState } from 'react';
import { KeyRound, Plus, Search, Trash2, Upload } from 'lucide-react';
import { Button, CustomSelect } from '@components/ui';
import Switch from '@components/desktop/Switch.jsx';
import ListEmpty from '@components/desktop/ListEmpty.jsx';
import RowMoreButton from '@components/desktop/RowMoreButton.jsx';
import useContextMenu from '@components/desktop/useContextMenu.jsx';
import { ExactSecretSheet, ImportCandidatesSheet, ManualImportSheet } from './PrivacyExactSecretSheets.jsx';
import {
  PRIVACY_EXACT_SECRET_CATEGORY_OPTIONS,
  createEmptyExactSecretForm,
  exactSecretCategoryLabel,
  exactSecretMinLength,
  exactSecretToForm,
  filterPrivacyExactSecrets,
  validateExactSecretForm
} from '../utils/privacyRules.js';
import {
  ClearExactSecretsDialog,
  DeleteExactSecretDialog
} from './PrivacyExactSecretActionDialogs.jsx';

const EXACT_SECRET_STATUS_OPTIONS = [
  { value: '', label: '全部状态' },
  { value: 'enabled', label: '已启用' },
  { value: 'disabled', label: '已禁用' }
];

const EXACT_SECRET_CATEGORY_FILTER_OPTIONS = [
  { value: '', label: '全部分类' },
  ...PRIVACY_EXACT_SECRET_CATEGORY_OPTIONS
];

const placeholderForCategory = (category) => {
  if (category === 'api_key') return '[API密钥]';
  if (category === 'token') return '[Token]';
  if (category === 'password') return '[密码]';
  return '[敏感值]';
};

const ExactSecretDialog = ({ open, secret, saving, onSave, onClose }) => {
  const [form, setForm] = useState(() => exactSecretToForm(secret || createEmptyExactSecretForm()));
  const [errors, setErrors] = useState({});
  const [submitError, setSubmitError] = useState('');
  const nameInputRef = useRef(null);
  const handleRequestClose = () => {
    if (!saving) onClose();
  };


  if (!open) return null;

  const update = (patch) => {
    setForm((prev) => ({ ...prev, ...patch }));
    setErrors((prev) => {
      const next = { ...prev };
      Object.keys(patch).forEach((key) => delete next[key]);
      return next;
    });
  };

  const handleCategoryChange = (category) => {
    update({
      category,
      placeholder: form.placeholder && form.placeholder !== placeholderForCategory(form.category)
        ? form.placeholder
        : placeholderForCategory(category)
    });
  };

  const handleSubmit = async (event) => {
    event.preventDefault();
    const nextErrors = validateExactSecretForm(form, { requireSecretValue: !(form.id > 0) });
    setErrors(nextErrors);
    if (Object.keys(nextErrors).length > 0) return;
    setSubmitError('');
    try {
      await onSave(form);
      onClose();
    } catch (err) {
      const message = err.message || String(err);
      setSubmitError(message.includes('privacy_exact_secret_exists') ? '该敏感值已存在' : message);
    }
  };

  return (
    <ExactSecretSheet open={open} form={form} errors={errors} submitError={submitError} saving={saving}
      initialFocusRef={nameInputRef} onClose={handleRequestClose} onSubmit={handleSubmit} update={update} onCategoryChange={handleCategoryChange} />
  );
};

const ImportCandidatesDialog = ({ open, loading, candidates, onLoad, onImport, onClose }) => {
  const [importingKey, setImportingKey] = useState('');
  const [error, setError] = useState('');
  const refreshRef = useRef(null);
  const handleRequestClose = () => {
    if (!importingKey) onClose();
  };


  if (!open) return null;

  const handleImport = async (candidate) => {
    const key = `${candidate.source_type}:${candidate.source_ref}`;
    setImportingKey(key);
    setError('');
    try {
      await onImport({
        source_type: candidate.source_type,
        source_ref: candidate.source_ref,
        name: candidate.name,
        category: candidate.category,
        placeholder: placeholderForCategory(candidate.category)
      });
    } catch (err) {
      const message = err.message || String(err);
      setError(message.includes('privacy_exact_secret_exists') ? '该敏感值已存在' : message);
    } finally {
      setImportingKey('');
    }
  };

  return (
    <ImportCandidatesSheet open={open} loading={loading} candidates={candidates} importingKey={importingKey} error={error}
      initialFocusRef={refreshRef} onClose={handleRequestClose} onLoad={onLoad} onImport={handleImport} />
  );
};

const ManualImportDialog = ({ open, onImport, onClose }) => {
  const [form, setForm] = useState({
    namePrefix: '手动敏感值',
    category: 'api_key',
    placeholder: placeholderForCategory('api_key'),
    description: '',
    values: ''
  });
  const [importing, setImporting] = useState(false);
  const [error, setError] = useState('');
  const valuesInputRef = useRef(null);
  const handleRequestClose = () => {
    if (!importing) onClose();
  };


  if (!open) return null;

  const update = (patch) => {
    setForm((prev) => ({ ...prev, ...patch }));
    setError('');
  };

  const handleCategoryChange = (category) => {
    update({
      category,
      placeholder: form.placeholder && form.placeholder !== placeholderForCategory(form.category)
        ? form.placeholder
        : placeholderForCategory(category)
    });
  };

  const lines = () => Array.from(new Set(
    String(form.values || '')
      .split(/\r?\n/)
      .map((line) => line.trim())
      .filter(Boolean)
  ));

  const handleSubmit = async (event) => {
    event.preventDefault();
    const values = lines();
    if (values.length === 0) {
      setError('请粘贴至少一行敏感值');
      return;
    }
    const minLength = exactSecretMinLength(form.category);
    const tooShort = values.find((value) => value.length < minLength);
    if (tooShort) {
      setError(`当前分类至少 ${minLength} 个字符，请检查过短条目`);
      return;
    }
    if (!String(form.placeholder || '').trim()) {
      setError('占位符不能为空');
      return;
    }

    const baseName = String(form.namePrefix || '').trim() || '手动敏感值';
    setImporting(true);
    setError('');
    let imported = 0;
    let skipped = 0;
    const failures = [];
    for (const [index, value] of values.entries()) {
      try {
        await onImport({
          source_type: 'manual',
          source_ref: '',
          name: values.length === 1 ? baseName : `${baseName} ${index + 1}`,
          category: form.category,
          placeholder: String(form.placeholder || '').trim(),
          description: String(form.description || '').trim(),
          secret_value: value
        });
        imported += 1;
      } catch (err) {
        const message = err.message || String(err);
        if (message.includes('privacy_exact_secret_exists')) {
          skipped += 1;
        } else {
          failures.push(`#${index + 1}: ${message}`);
        }
      }
    }
    setImporting(false);

    if (failures.length > 0 || skipped > 0) {
      const parts = [];
      if (imported > 0) parts.push(`已导入 ${imported} 条`);
      if (skipped > 0) parts.push(`跳过 ${skipped} 条重复`);
      if (failures.length > 0) parts.push(`失败 ${failures.length} 条：${failures.slice(0, 2).join('；')}`);
      setError(parts.join('，'));
      return;
    }

    setForm((prev) => ({ ...prev, values: '' }));
    onClose();
  };

  return (
    <ManualImportSheet open={open} form={form} importing={importing} error={error}
      initialFocusRef={valuesInputRef} onClose={handleRequestClose} onSubmit={handleSubmit} update={update} onCategoryChange={handleCategoryChange} />
  );
};

const PrivacyExactSecretsPanel = ({
  secrets = [],
  busy,
  onSave,
  onDelete,
  onClear,
  onLoadCandidates,
  onImportCandidate
}) => {
  const [dialogOpen, setDialogOpen] = useState(false);
  const [dialogSecret, setDialogSecret] = useState(null);
  const [dialogKey, setDialogKey] = useState(0);
  const [importOpen, setImportOpen] = useState(false);
  const [manualImportOpen, setManualImportOpen] = useState(false);
  const [candidateLoading, setCandidateLoading] = useState(false);
  const [candidates, setCandidates] = useState([]);
  const [filters, setFilters] = useState({});
  const [panelError, setPanelError] = useState('');
  const [deleteTarget, setDeleteTarget] = useState(null);
  const [deleteBusy, setDeleteBusy] = useState(false);
  const [clearOpen, setClearOpen] = useState(false);
  const [clearBusy, setClearBusy] = useState(false);
  const filteredSecrets = useMemo(() => filterPrivacyExactSecrets(secrets, filters), [secrets, filters]);
  const actionBusy = busy || deleteBusy || clearBusy;
  const { contextMenu, openContextMenu } = useContextMenu();

  const updateFilters = (patch) => {
    setFilters((prev) => ({ ...prev, ...patch }));
  };

  const openDialog = (secret) => {
    setDialogSecret(secret);
    setDialogKey((key) => key + 1);
    setDialogOpen(true);
  };

  const handleToggle = async (secret, enabled) => {
    setPanelError('');
    try {
      await onSave({ ...secret, enabled, secret_value: '' });
    } catch (err) {
      setPanelError(err.message || String(err));
    }
  };

  const handleDelete = (secret) => {
    setPanelError('');
    setDeleteTarget(secret);
  };

  const confirmDelete = async () => {
    if (!deleteTarget) return;
    setDeleteBusy(true);
    setPanelError('');
    try {
      await onDelete(deleteTarget.id);
      setDeleteTarget(null);
    } catch (err) {
      setPanelError(err.message || String(err));
    } finally {
      setDeleteBusy(false);
    }
  };

  const confirmClear = async (confirmText) => {
    setClearBusy(true);
    setPanelError('');
    try {
      await onClear(confirmText);
      setClearOpen(false);
    } catch (err) {
      setPanelError(err.message || String(err));
    } finally {
      setClearBusy(false);
    }
  };

  const loadCandidates = async () => {
    setCandidateLoading(true);
    setPanelError('');
    try {
      setCandidates(await onLoadCandidates());
    } catch (err) {
      setPanelError(err.message || String(err));
    } finally {
      setCandidateLoading(false);
    }
  };

  const openImport = () => {
    setImportOpen(true);
    loadCandidates();
  };

  const openRowMenu = (event, secret) => openContextMenu(event, [
    { label: '编辑…', disabled: actionBusy, onSelect: () => openDialog(exactSecretToForm(secret)) },
    { label: secret.enabled ? '停用' : '启用', disabled: actionBusy, onSelect: () => handleToggle(secret, !secret.enabled) },
    '-',
    { label: '删除…', danger: true, disabled: actionBusy, onSelect: () => handleDelete(secret) }
  ]);

  return (
    <div className="flex min-h-0 flex-1 flex-col">
      {/* 操作与筛选：一条范围条 */}
      <div className="flex shrink-0 flex-wrap items-center gap-1.5 border-b border-line bg-inspector px-3 py-1.5">
        <Button size="sm" icon={Plus} onClick={() => openDialog(createEmptyExactSecretForm())} disabled={actionBusy}>
          新增敏感值
        </Button>
        <Button size="sm" variant="secondary" icon={Upload} onClick={openImport} disabled={actionBusy}>
          从账号/端点导入
        </Button>
        <Button size="sm" variant="secondary" icon={Upload} onClick={() => setManualImportOpen(true)} disabled={actionBusy}>
          粘贴导入
        </Button>
        <Button size="sm" variant="secondary" icon={Trash2} onClick={() => setClearOpen(true)} disabled={actionBusy || secrets.length === 0}>
          清空
        </Button>
        {secrets.length > 0 && (
          <>
            <span className="mx-1 h-4 w-px bg-line" aria-hidden="true" />
            <div className="relative w-52">
              <Search size={13} className="pointer-events-none absolute left-2 top-1/2 -translate-y-1/2 text-fg-subtle" />
              <input
                aria-label="搜索本地敏感值"
                value={filters.keyword || ''}
                onChange={(e) => updateFilters({ keyword: e.target.value })}
                placeholder="搜索名称 / 描述 / 掩码 / 来源"
                className="desk-field w-full pl-7 text-xs placeholder:text-fg-subtle"
              />
            </div>
            <CustomSelect
              size="xs"
              options={EXACT_SECRET_CATEGORY_FILTER_OPTIONS}
              value={filters.category || ''}
              onChange={(category) => updateFilters({ category })}
              className="w-24"
            />
            <CustomSelect
              size="xs"
              options={EXACT_SECRET_STATUS_OPTIONS}
              value={filters.enabled || ''}
              onChange={(enabled) => updateFilters({ enabled })}
              className="w-24"
            />
            <span className="ml-auto text-xs tabular-nums text-fg-subtle">
              {filteredSecrets.length} / {secrets.length} 条
            </span>
          </>
        )}
      </div>
      {panelError && (
        <div role="alert" className="selectable shrink-0 break-all border-b border-danger-line bg-danger-soft px-4 py-1.5 text-xs text-danger">{panelError}</div>
      )}

      <div className="min-h-0 flex-1 overflow-auto">
        {secrets.length === 0 ? (
          <ListEmpty
            icon={KeyRound}
            title="暂无本地敏感值"
            description="手动加入真实 key、token、密码或固定敏感文本后，才会按精确字符串替换。完整敏感值不在列表中展示，也不会导出到规则 JSON。"
          />
        ) : filteredSecrets.length === 0 ? (
          <ListEmpty icon={Search} title="没有匹配的本地敏感值" description="调整搜索词、分类或状态筛选后再看" />
        ) : (
          <table className="desk-table">
            <thead>
              <tr>
                <th className="w-[52px] text-center">启用</th>
                <th>名称</th>
                <th>分类</th>
                <th title="完整敏感值不在列表中展示，也不会导出到规则 JSON">掩码值</th>
                <th>占位符</th>
                <th>来源</th>
                <th className="w-[36px]" aria-label="操作" />
              </tr>
            </thead>
            <tbody>
              {filteredSecrets.map((secret) => (
                <tr
                  key={secret.id}
                  onDoubleClick={() => !actionBusy && openDialog(exactSecretToForm(secret))}
                  onContextMenu={(event) => openRowMenu(event, secret)}
                  className={secret.enabled ? '' : 'text-fg-subtle'}
                >
                  <td className="text-center">
                    <span className="flex justify-center">
                      <Switch checked={secret.enabled} disabled={actionBusy} label={`${secret.name} 启用`} onChange={(enabled) => handleToggle(secret, enabled)} />
                    </span>
                  </td>
                  <td className="max-w-[240px] py-1.5">
                    <div className={`truncate font-medium ${secret.enabled ? 'text-fg' : ''}`} title={secret.name}>{secret.name}</div>
                    {secret.description && <div className="truncate text-xs text-fg-subtle" title={secret.description}>{secret.description}</div>}
                  </td>
                  <td className="text-xs text-fg-body">{exactSecretCategoryLabel(secret.category)}</td>
                  <td className="text-xs text-fg-muted">
                    <span className="font-mono">{secret.masked_value}</span>
                    <span className="ml-2 text-fg-subtle">{secret.value_length} 字符</span>
                  </td>
                  <td className="font-mono text-xs text-fg-body">{secret.placeholder}</td>
                  <td className="text-xs text-fg-subtle">{secret.source_type}</td>
                  <td><RowMoreButton onOpenMenu={(event) => openRowMenu(event, secret)} /></td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </div>
      {contextMenu}

      {deleteTarget && (
        <DeleteExactSecretDialog
          secret={deleteTarget}
          loading={deleteBusy}
          onConfirm={confirmDelete}
          onClose={() => setDeleteTarget(null)}
        />
      )}
      {clearOpen && (
        <ClearExactSecretsDialog
          loading={clearBusy}
          onConfirm={confirmClear}
          onClose={() => setClearOpen(false)}
        />
      )}

      <ExactSecretDialog
        key={dialogKey}
        open={dialogOpen}
        secret={dialogSecret}
        saving={actionBusy}
        onSave={onSave}
        onClose={() => setDialogOpen(false)}
      />
      <ManualImportDialog
        open={manualImportOpen}
        onImport={onImportCandidate}
        onClose={() => setManualImportOpen(false)}
      />
      <ImportCandidatesDialog
        open={importOpen}
        loading={candidateLoading}
        candidates={candidates}
        onLoad={loadCandidates}
        onImport={async (input) => {
          await onImportCandidate(input);
          await loadCandidates();
        }}
        onClose={() => setImportOpen(false)}
      />
    </div>
  );
};

export default PrivacyExactSecretsPanel;
