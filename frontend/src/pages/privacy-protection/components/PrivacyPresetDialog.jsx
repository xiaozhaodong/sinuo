// ============================================
// 隐私预设导入弹窗
// 2026-06-11 (v6.1 新增)
// ============================================

import { useRef, useState } from 'react';
import { PackagePlus } from 'lucide-react';
import { Button } from '@components/ui';
import Sheet from '@components/desktop/Sheet.jsx';
import { FormBlock, FormSection } from '@components/desktop/FormSection.jsx';

const PrivacyPresetDialog = ({ open, presets = [], onImport, onClose }) => {
  const [selectedId, setSelectedId] = useState('');
  const [importingId, setImportingId] = useState('');
  const [message, setMessage] = useState('');
  const [error, setError] = useState('');
  const presetsRef = useRef(null);
  const handleRequestClose = () => {
    if (!importingId) onClose();
  };


  if (!open) return null;

  const selectedPreset = presets.find((preset) => preset.id === selectedId) || presets[0];

  const handleImport = async (preset) => {
    if (!preset) return;
    setImportingId(preset.id);
    setMessage('');
    setError('');
    try {
      const created = await onImport(preset.id);
      if (!created || created.length === 0) {
        setMessage(`「${preset.name}」中的预设规则已是最新`);
      } else {
        setMessage(`已导入或同步 ${created.length} 条规则并立即编译生效`);
      }
    } catch (err) {
      setError(err.message || String(err));
    } finally {
      setImportingId('');
    }
  };

  return (
    <Sheet open={open} title="导入预设规则集" label="导入预设规则集" width={512} closeLabel="关闭弹窗"
      onClose={handleRequestClose} initialFocusRef={presetsRef}
      footerStart={<>{message && <span className="text-success">{message}</span>}{error && <span role="alert" className="break-all text-danger">{error}</span>}</>}
      footer={<>
        <Button type="button" variant="ghost" onClick={handleRequestClose} disabled={Boolean(importingId)}>取消</Button>
        <Button type="button" icon={PackagePlus} loading={Boolean(importingId)} disabled={!selectedPreset || Boolean(importingId)} onClick={() => handleImport(selectedPreset)}>导入选中预设</Button>
      </>}>
      <FormSection title="导入预设规则集">
        <div ref={presetsRef} tabIndex={-1} className="divide-y divide-line-soft outline-none">
          {presets.map((preset) => (
            <button key={preset.id} type="button" disabled={Boolean(importingId)}
              aria-pressed={selectedPreset?.id === preset.id}
              onClick={() => { setSelectedId(preset.id); setMessage(''); setError(''); }}
              className={`flex w-full items-center gap-3 px-3.5 py-2 text-left transition-colors ${selectedPreset?.id === preset.id ? 'bg-accent-soft' : 'hover:bg-surface-sub'} ${importingId ? 'cursor-not-allowed opacity-70' : ''}`}>
              <span className="min-w-0 flex-1">
                <span className="block text-[13px] font-medium text-fg">{preset.name}</span>
                <span className="block text-xs leading-5 text-fg-muted">{preset.description} · 共 {preset.rule_count} 条</span>
              </span>
              <span aria-hidden="true" className={`h-3 w-3 shrink-0 rounded-full border ${selectedPreset?.id === preset.id ? 'border-accent-ring bg-accent-ring' : 'border-line-strong bg-surface'}`} />
            </button>
          ))}
          {presets.length === 0 && <p className="py-4 text-center text-xs text-fg-muted">暂无可用预设</p>}
        </div>
        <FormBlock><p className="text-xs text-fg-muted">同名自定义规则不会覆盖；同名预设规则会同步到最新版。导入后立即编译热生效。</p></FormBlock>
      </FormSection>
    </Sheet>
  );
};
export default PrivacyPresetDialog;
