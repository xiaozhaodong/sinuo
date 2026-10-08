import { useRef, useState } from 'react';
import { Button } from '@components/ui';
import Sheet from '@components/desktop/Sheet.jsx';
import { resolveErrorMessage } from '@utils/errors.js';

const CodexImportSheet = ({ preview, saving, onImport, onClose }) => {
  const [error, setError] = useState('');
  const importRef = useRef(null);
  const apply = async () => {
    setError('');
    try {
      await onImport();
    } catch (failure) {
      setError(resolveErrorMessage(failure, '应用导入失败'));
    }
  };

  return (
    <Sheet title="导入 Codex 目录" subtitle={preview.name} width={520} onClose={onClose} closeDisabled={saving} initialFocusRef={importRef}
      footerStart={error && <span role="alert" className="text-danger">{error}</span>}
      footer={<><Button type="button" variant="secondary" disabled={saving} onClick={onClose}>取消</Button><Button ref={importRef} type="button" loading={saving} onClick={apply}>导入并保存</Button></>}
    >
      <p className="mt-3 text-xs text-fg-body">
        {preview.models.length} 个模型，新增 {preview.models.length - preview.conflicts.length} 个，更新 {preview.conflicts.length} 个同名模型的名称、描述和能力。
      </p>
      {preview.conflicts.length > 0 && (
        <p className="mt-2 max-h-40 overflow-y-auto break-all text-xs leading-5 text-fg-muted">更新：{preview.conflicts.join('、')}。现有启用状态保持不变。</p>
      )}
    </Sheet>
  );
};

export default CodexImportSheet;
