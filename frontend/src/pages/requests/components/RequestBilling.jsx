import { useEffect, useRef, useState } from 'react';
import { Button } from '@components/ui';
import { PropertyList, Property } from '@components/desktop/Inspector.jsx';
import { formatCost } from '@utils/api.js';
import { resolveErrorMessage } from '@utils/errors.js';
import { getOfficialCost } from '@components/billing/pricingApi.js';

const SPEED_LABELS = { fast: 'Fast', standard: '标准' };
const MULTIPLIER_LABELS = [
  ['input', '输入'],
  ['output', '输出'],
  ['cache_read', '缓存读取'],
  ['cache_creation_5m', '写入 5m'],
  ['cache_creation_1h', '写入 1h'],
  ['cache_creation_30m', '写入 30m'],
  ['cache_creation_12h', '写入 12h']
];
const speedLabel = (speed) => SPEED_LABELS[speed] || '未确定';
const levelLabel = (level) => (level ? `长档 ${level}` : '基础档');
const tierText = (period, level) => [period || '全天', levelLabel(level)].join(' · ');
const estimateLabel = (speed) => (speed === 'fast' ? '按请求 Fast 估算' : '标准档估算');

function formatDecisionTier(decision, usageKnown) {
  const knownSpeed = SPEED_LABELS[decision.speed] || '';
  if (!usageKnown) {
    return `${speedLabel(decision.speed)} · 用量未知，未判定上下文档位`;
  }
  const settledRow = (decision.channel_row ?? 0) > 0;
  if (!settledRow && decision.missing) {
    return knownSpeed ? `未判定档位 · ${knownSpeed}` : '未判定档位';
  }
  const parts = [tierText(decision.channel_period, decision.channel_tier)];
  if (knownSpeed) {
    parts.push(knownSpeed);
  } else if (!settledRow) {
    parts.push('未确定');
  }
  return parts.join(' · ');
}

// 请求已结算后的计费依据。只放在「Token 与成本」下方，不再自成分区。
export default function RequestBilling({ id, version, decision, pending, usageKnown, officialRecalculation, billingMultipliers }) {
  const [expanded, setExpanded] = useState(false);
  const [reference, setReference] = useState(null);
  const [error, setError] = useState('');
  const [loading, setLoading] = useState(false);
  const mountedRef = useRef(true);
  const seqRef = useRef(0);
  useEffect(() => {
    mountedRef.current = true;
    return () => {
      mountedRef.current = false;
    };
  }, []);
  const toggleReference = async () => {
    setExpanded(!expanded);
    if (expanded || loading || reference) return;
    const seq = ++seqRef.current;
    setLoading(true);
    setError('');
    try {
      const value = await getOfficialCost(id);
      if (!Number.isFinite(value?.TotalCost)) throw new Error('缺少可复算的官方金额');
      if (mountedRef.current && seqRef.current === seq) setReference(value);
    } catch (err) {
      if (mountedRef.current && seqRef.current === seq) {
        setError(resolveErrorMessage(err, '官方价复算失败'));
      }
    } finally {
      if (mountedRef.current && seqRef.current === seq) setLoading(false);
    }
  };
  if (pending) return <p className="text-xs text-fg-muted">等待请求结算</p>;
  const unified = Number.isFinite(billingMultipliers?.unified);
  const split = unified ? [] : MULTIPLIER_LABELS.filter(([key]) => Number.isFinite(billingMultipliers?.[key]));
  const showMultipliers = unified || split.length > 0;
  if (!decision && !officialRecalculation && !showMultipliers) return null;
  const showRule = Number.isFinite(decision?.model_multiplier) && (decision.rule_id > 0 || decision.model_multiplier !== 1);
  return (
    <div className="space-y-2 text-xs">
      {(decision || showMultipliers) && (
        <PropertyList labelWidth={64} align="start">
          {decision && (
            <>
              <Property label="计费档位" truncate={false} title={version > 0 ? `费率版本 #${version}` : undefined}>
                {formatDecisionTier(decision, usageKnown)}
                {decision.estimated && (
                  <span className="ml-2 rounded border border-warn-line px-1.5 text-warn">
                    {estimateLabel(decision.speed)}
                  </span>
                )}
              </Property>
              {usageKnown && Number.isFinite(decision.full_input) && (
                <Property label="完整输入">{`${Number(decision.full_input).toLocaleString()} Token`}</Property>
              )}
              {showRule && <Property label="模型规则">×{decision.model_multiplier}</Property>}
            </>
          )}
          {showMultipliers && (
            <Property label="渠道倍率" truncate={false}>
              {unified ? `统一 ×${billingMultipliers.unified}` : (
                <div className="flex flex-wrap gap-x-3 gap-y-1">
                  {split.map(([key, label]) => (
                    <span key={key} className="whitespace-nowrap">{`${label} ×${billingMultipliers[key]}`}</span>
                  ))}
                </div>
              )}
            </Property>
          )}
        </PropertyList>
      )}
      {decision?.missing && <p className="text-warn">{decision.missing}</p>}
      {officialRecalculation?.available === true && (
        <Button type="button" size="sm" variant="secondary" aria-expanded={expanded} onClick={toggleReference}>
          {expanded ? '收起复算结果' : '按当时官方价复算'}
        </Button>
      )}
      {officialRecalculation?.available === false && (
        <p className="text-fg-muted">
          官方参考费用不可复算{officialRecalculation.missing && ` · ${officialRecalculation.missing}`}
        </p>
      )}
      {officialRecalculation?.available && expanded && loading && <p role="status" className="text-fg-muted">正在复算…</p>}
      {officialRecalculation?.available && expanded && error && (
        <p role="alert" className="text-warn">
          {error}
        </p>
      )}
      {officialRecalculation?.available && expanded && reference && (
        <div role="status" className="space-y-1 rounded border border-line bg-surface px-3 py-2">
          <div className="flex items-center justify-between gap-2">
            <span className="text-fg-muted">官方参考费用</span>
            <strong className="selectable font-mono text-fg">{formatCost(reference.TotalCost)}</strong>
          </div>
          {reference.decision && (
            <p className="text-fg-muted">
              {[
                tierText(reference.decision.official_period, reference.decision.official_tier),
                reference.decision.estimated
                  ? estimateLabel(reference.decision.speed)
                  : SPEED_LABELS[reference.decision.speed]
              ].filter(Boolean).join(' · ')}
            </p>
          )}
        </div>
      )}
    </div>
  );
}
