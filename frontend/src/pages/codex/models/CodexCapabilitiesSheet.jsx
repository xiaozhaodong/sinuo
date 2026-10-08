import { useRef, useState } from 'react';
import { Button } from '@components/ui';
import Sheet from '@components/desktop/Sheet.jsx';
import { resolveErrorMessage } from '@utils/errors.js';
import { parseCapabilities, validateCapabilities } from '../../account-pool/utils/codexModelCatalog.js';

const CodexCapabilitiesSheet = ({ modelId, metadata, rawCapabilities, onApply, onClose }) => {
  const [text, setText] = useState(() => rawCapabilities ?? (metadata ? JSON.stringify(metadata, null, 2) : ''));
  const [error, setError] = useState('');
  const inputRef = useRef(null);

  const apply = () => {
    try {
      const next = parseCapabilities({ capabilities_json: text });
      if (next) validateCapabilities(next);
      onApply(next);
      onClose();
    } catch (failure) {
      setError(resolveErrorMessage(failure, '能力 JSON 校验失败'));
    }
  };

  return (
    <Sheet title="编辑能力 JSON" subtitle={modelId || '新建模型'} width={720} onClose={onClose} initialFocusRef={inputRef}
      footerStart={error && <span role="alert" className="text-danger">{error}</span>}
      footer={<><Button type="button" variant="secondary" onClick={onClose}>取消</Button><Button type="button" onClick={apply}>应用</Button></>}
    >
      <p className="mt-3 text-[11.5px] text-fg-muted">ID、名称、描述使用检查器里的基本信息；清空内容表示不提供能力。</p>
      <textarea ref={inputRef} aria-label="高级能力 JSON" spellCheck={false} placeholder="{ }"
        className="desk-field mt-2 h-auto min-h-[360px] w-full py-1.5 font-mono text-xs leading-relaxed"
        value={text} onChange={(event) => { setText(event.target.value); setError(''); }} />
    </Sheet>
  );
};

export default CodexCapabilitiesSheet;
