// ============================================
// 隐私规则测试面板
// 2026-06-11 (v6.1 新增)
// 测试文本只保存在 React state，不写 localStorage、不落请求日志
// ============================================

import { useState } from 'react';
import { AlertCircle } from 'lucide-react';
import { Button, CustomSelect } from '@components/ui';
import { FormBlock, FormFieldRow, FormSection } from '@components/desktop/FormSection.jsx';
import PrivacyHitList from './PrivacyHitList.jsx';
import {
  PRIVACY_PATH_OPTIONS,
  PRIVACY_UPSTREAM_TYPE_OPTIONS
} from '../utils/privacyRules.js';

const PrivacyRuleTestPanel = ({ onTest, variant = 'card', showHeader = true }) => {
  const [text, setText] = useState('');
  const [path, setPath] = useState('/v1/messages');
  const [upstreamType, setUpstreamType] = useState('endpoint');
  const [testing, setTesting] = useState(false);
  const [result, setResult] = useState(null);
  const [error, setError] = useState('');
  const isDrawer = variant === 'drawer';

  const handleTest = async () => {
    setTesting(true);
    setError('');
    try {
      setResult(await onTest({ text, path, upstream_type: upstreamType }));
    } catch (err) {
      setResult(null);
      setError(err.message || String(err));
    } finally {
      setTesting(false);
    }
  };

  return (
    <div>
      {showHeader && (
        <div className="mx-1 mt-5 flex items-baseline justify-between gap-2">
          <h3 className="text-[13px] font-semibold text-fg-body">规则测试</h3>
          <span className="text-xs text-fg-muted">测试文本不会被记录</span>
        </div>
      )}
      <FormSection title="测试输入">
        <FormFieldRow label="请求路径">
          <CustomSelect
            options={PRIVACY_PATH_OPTIONS.map((p) => ({ value: p, label: p }))}
            value={path}
            onChange={setPath}
            size="sm"
            className="w-full"
          />
        </FormFieldRow>
        <FormFieldRow label="上游类型">
          <CustomSelect options={PRIVACY_UPSTREAM_TYPE_OPTIONS} value={upstreamType} onChange={setUpstreamType} size="sm" className="w-full" />
        </FormFieldRow>
        <FormBlock>
          <textarea
            value={text}
            onChange={(e) => setText(e.target.value)}
            rows={isDrawer ? 9 : 6}
            spellCheck={false}
            placeholder="粘贴一段测试文本，例如包含 API Key 或手机号的内容"
            className="desk-field h-auto w-full break-all py-1.5 font-mono leading-relaxed"
          />
          <div className="mt-3 flex justify-end">
            <Button onClick={handleTest} loading={testing} disabled={!text} size="sm">测试</Button>
          </div>
          {error && (
            <div className="mt-3 flex items-start gap-2 rounded-lg border border-danger-line bg-danger-soft px-3 py-2 text-xs text-danger">
              <AlertCircle size={14} className="mt-0.5 shrink-0" /><span className="break-all">{error}</span>
            </div>
          )}
        </FormBlock>
      </FormSection>
      {result && (
        <FormSection title="测试结果" actions={(
          <>
            <span className="tone-slate rounded border px-1.5 text-[11px] leading-[18px]">原文 {result.original_length} 字符</span>
            <span className="tone-indigo rounded border px-1.5 text-[11px] leading-[18px]">命中 {result.hit_count} 处</span>
            <span className="rounded border border-line px-1.5 text-[11px] leading-[18px] text-fg-muted">耗时 {result.scan_duration_ms.toFixed(2)} ms</span>
          </>
        )}>
          <FormBlock>
            <div className="mb-1 text-xs font-medium text-fg-muted">替换后文本</div>
            <pre className={`whitespace-pre-wrap break-all overflow-y-auto rounded-md border border-line-soft bg-pane px-3 py-2 font-mono text-xs ${isDrawer ? 'max-h-72' : 'max-h-48'}`}>
              {result.replaced_text || '（空）'}
            </pre>
          </FormBlock>
          <FormBlock>
            <div className="mb-1 text-xs font-medium text-fg-muted">命中规则</div>
            <PrivacyHitList hits={result.rule_hits} />
          </FormBlock>
        </FormSection>
      )}
    </div>
  );
};

export default PrivacyRuleTestPanel;
