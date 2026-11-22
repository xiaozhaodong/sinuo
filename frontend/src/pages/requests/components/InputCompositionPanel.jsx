import { useMemo, useState } from 'react';
import { ArrowRight } from 'lucide-react';
import { distributeCells, formatCompact, formatExact, formatPercent } from '../utils/inputComposition.js';

const MATRIX_CELLS = 240; // 30 × 8（窄容器 24 × 10）
const WARN_RATIO = 0.8;
const FULL_RATIO = 0.95;

const occupancyTone = (ratio) => {
  if (ratio == null) return 'border-line bg-surface-sub text-fg-muted';
  if (ratio >= FULL_RATIO) return 'border-danger-line bg-danger-soft text-danger';
  if (ratio >= WARN_RATIO) return 'border-warn-line bg-warn-soft text-warn';
  return 'border-line bg-surface-sub text-fg-body';
};

const occupancyTextTone = (ratio) => {
  if (ratio == null) return 'text-fg-muted';
  if (ratio >= FULL_RATIO) return 'text-danger';
  if (ratio >= WARN_RATIO) return 'text-warn';
  return 'text-fg-muted';
};

function buildParts(groups, total, limit) {
  const parts = groups.filter((g) => g.tokens > 0);
  if (limit && limit > total) {
    parts.push({ key: 'free', name: '剩余窗口', tokens: limit - total, color: 'var(--ctx-free)', free: true });
  }
  return parts;
}

/**
 * 嵌入详情面板 36px 顶栏的轻量页签切换：零额外纵向高度，切换请求时保持所选视图。
 */
export function ContextViewTabs({ data, value, onChange }) {
  const ratio = data?.window ? data.total / data.window : null;
  const badge = data && !data.pendingTotal && (ratio !== null ? formatPercent(data.total, data.window) : formatCompact(data.total));
  const badgeTone = ratio !== null && ratio >= FULL_RATIO
    ? 'text-danger'
    : ratio !== null && ratio >= WARN_RATIO
      ? 'text-warn'
      : 'text-fg-muted';

  const tabs = [
    { key: 'detail', label: '概览' },
    { key: 'context', label: '输入构成', badge, badgeTone, estimating: data && !data.counted }
  ];

  return (
    <div
      role="tablist"
      aria-label="请求详情视图"
      className="ml-1 inline-flex shrink-0 items-center rounded-[7px] bg-surface-mut p-0.5"
    >
      {tabs.map((tab) => {
        const active = value === tab.key;
        return (
          <button
            key={tab.key}
            type="button"
            role="tab"
            tabIndex={active ? 0 : -1}
            onKeyDown={(event) => {
              if (!['ArrowLeft', 'ArrowRight', 'Home', 'End'].includes(event.key)) return;
              event.preventDefault();
              event.stopPropagation();
              const index = event.key === 'Home' ? 0 : event.key === 'End' ? 1 : Number(tab.key === 'detail');
              onChange(tabs[index].key);
              event.currentTarget.parentElement.children[index].focus();
            }}
            aria-selected={active}
            onClick={() => onChange(tab.key)}
            className={`inline-flex h-[22px] items-center gap-1.5 whitespace-nowrap rounded-[5px] px-2 text-xs transition-colors ${
              active
                ? 'bg-surface font-medium text-fg shadow-xs ring-1 ring-hairline dark:bg-surface-emph'
                : 'text-fg-body hover:text-fg'
            }`}
          >
            <span>{tab.label}</span>
            {tab.badge && (
              <span className={`inline-flex items-center gap-1 font-mono text-[10.5px] tabular-nums ${active ? badgeTone : 'text-fg-subtle'}`}>
                {tab.estimating && <span className="h-1.5 w-1.5 rounded-full bg-warn-solid" title="估算中" />}
                {tab.badge}
              </span>
            )}
          </button>
        );
      })}
    </div>
  );
}

/**
 * 嵌入「概览 → Token 与成本」卡片下方的迷你构成条，让 Token 明细与输入构成自然衔接。
 */
export function ContextMiniBar({ data, onOpen }) {
  if (!data || data.pendingTotal) return null;
  const { groups, total, window: limit } = data;
  const largest = groups[0];
  const base = limit && limit > total ? limit : total;
  const ratio = limit ? total / limit : null;

  return (
    <button
      type="button"
      onClick={onOpen}
      className="group mt-2 block w-full rounded-lg border border-line bg-surface px-3 py-2 text-left transition-colors hover:border-line-strong hover:bg-surface-sub"
    >
      <div className="mb-1.5 flex items-center justify-between gap-2 text-[11.5px]">
        <span className="inline-flex min-w-0 items-center gap-1.5 text-fg-body">
          <span className="font-medium">输入构成</span>
          {largest && (
            <span className="truncate text-fg-muted">
              {largest.name} {formatPercent(largest.tokens, total)}
            </span>
          )}
        </span>
        <span className={`inline-flex shrink-0 items-center gap-1 font-mono text-[11px] tabular-nums ${occupancyTextTone(ratio)} group-hover:text-accent`}>
          {ratio !== null ? `窗口 ${formatPercent(total, limit)}` : '查看拆解'}
          <ArrowRight size={11} className="transition-transform group-hover:translate-x-0.5" />
        </span>
      </div>
      <div className="ctx-mini-bar" aria-hidden="true">
        {groups.filter((g) => g.tokens > 0).map((g) => (
          <span
            key={g.key}
            className="ctx-mini-seg"
            style={{ '--tile': g.color, width: `${(g.tokens / base) * 100}%` }}
            title={`${g.name} · ${formatExact(g.tokens)} tokens (${formatPercent(g.tokens, total)})`}
          />
        ))}
      </div>
    </button>
  );
}

/**
 * 同构宽屏三栏输入构成工作台：
 * 左栏（点阵与水位） ｜ 中栏（分类占比） ｜ 右栏（高占用 / 选中分类条目明细）
 * 无模态抽屉，支持在上方请求列表按 ↑/↓ 连续切换请求实时巡检。
 */
export function ContextWorkbench({ data, loading, error }) {
  const [selected, setSelected] = useState(null);
  const [hovered, setHovered] = useState(null);

  const cells = useMemo(() => {
    if (!data) return [];
    const parts = buildParts(data.groups, data.total, data.window);
    const counts = distributeCells(parts.map((p) => p.tokens), MATRIX_CELLS);
    return parts.flatMap((part, idx) => Array.from({ length: counts[idx] }, () => part));
  }, [data]);

  if (!data || data.pendingTotal) return (
    <div role="tabpanel" aria-label="输入构成" className="grid flex-1 place-content-center bg-inspector text-xs text-fg-muted">
      {loading ? '正在读取输入构成…' : error ? '输入构成读取失败' : data?.pendingTotal ? '当前没有可估算的可见输入，尚无上游用量。' : '此请求未采集输入构成'}
      {data?.pendingTotal && <span className="ml-1 cursor-help" title={data.attributionNote} aria-label={data.attributionNote} tabIndex={0}>ⓘ</span>}
    </div>
  );

  const { groups, total, window: limit, counted } = data;
  const ratio = limit ? total / limit : null;
  const freeTokens = limit ? Math.max(limit - total, 0) : 0;
  const activeKey = hovered || selected;
  const selectedGroup = groups.find((g) => g.key === selected) || null;
  const focusPart = activeKey === 'free'
    ? { key: 'free', name: '剩余窗口', tokens: freeTokens, color: 'var(--ctx-free)', free: true }
    : groups.find((g) => g.key === activeKey) || null;

  const allItems = groups
    .flatMap((g) => g.items.map((item) => ({ ...item, group: g })))
    .sort((a, b) => b.tokens - a.tokens);
  const visibleItems = selectedGroup
    ? selectedGroup.items.map((item) => ({ ...item, group: selectedGroup }))
    : allItems;
  const maxItemTokens = visibleItems[0]?.tokens || 1;

  const toggleSelect = (key) => {
    if (key === 'free') return;
    setSelected((prev) => (prev === key ? null : key));
  };

  // 左栏顶部指标：悬停/锁定任意分类时，主数字与右侧百分比直接联动切换到该分类
  const heroValue = focusPart ? formatCompact(focusPart.tokens) : formatCompact(total);
  const heroExact = focusPart ? formatExact(focusPart.tokens) : formatExact(total);
  const heroPill = focusPart
    ? (focusPart.free
      ? (limit ? formatPercent(focusPart.tokens, limit) : '—')
      : formatPercent(focusPart.tokens, total))
    : (limit ? formatPercent(total, limit) : '窗口未知');
  const heroPillTitle = focusPart
    ? (focusPart.free ? '剩余占模型窗口比例' : '占本次输入比例')
    : (limit ? '本次输入占模型窗口比例' : '模型窗口未知');
  const heroPillClass = focusPart
    ? 'border-line bg-surface-sub text-fg-body'
    : occupancyTone(ratio);

  const footerCaption = (() => {
    if (focusPart) {
      if (focusPart.free) {
        return `剩余窗口 · 占窗口 ${formatPercent(freeTokens, limit)}`;
      }
      return limit
        ? `占输入 ${formatPercent(focusPart.tokens, total)} · 占窗口 ${formatPercent(focusPart.tokens, limit)}`
        : `占本次输入 ${formatPercent(focusPart.tokens, total)}`;
    }
    if (!limit) return '模型窗口上限未知 · 按本次输入 100% 展示';
    if (total > limit) return `超出参考窗口 ${formatExact(total - limit)} tokens`;
    if (ratio >= WARN_RATIO) return `仅剩 ${formatExact(freeTokens)} tokens 可用`;
    return `剩余 ${formatExact(freeTokens)} tokens`;
  })();

  return (
    <div role="tabpanel" aria-label="输入构成" className="@container min-h-0 flex-1 overflow-y-auto @3xl:overflow-hidden">
      <div className="grid min-h-full grid-cols-1 gap-px bg-line @xl:grid-cols-2 @3xl:h-full @3xl:grid-cols-[1.22fr_0.96fr_1.12fr]">
        {/* 左栏：主指标 + 正方格矩阵 + 水位/校准底栏 */}
        <section className="custom-scrollbar flex min-w-0 flex-col bg-inspector px-4 py-3 @3xl:min-h-0 @3xl:overflow-y-auto">
          <div className="flex items-center justify-between gap-2">
            <div className="flex min-w-0 items-baseline gap-2" title={`${heroExact} tokens`}>
              {focusPart && (
                <span
                  className="mb-0.5 inline-block size-2 shrink-0 self-center rounded-[2px]"
                  style={{ background: focusPart.color }}
                />
              )}
              <span className="font-mono text-[21px] font-semibold leading-none tracking-[-0.02em] text-fg tabular-nums">
                {heroValue}
              </span>
              <span className="truncate text-xs text-fg-muted" title={focusPart ? undefined : data.windowDescription}>
                {focusPart
                  ? focusPart.name
                  : limit
                    ? `/ ${formatCompact(limit)} tokens`
                    : 'tokens · 窗口未知'}
              </span>
            </div>
            <div className="flex shrink-0 items-center gap-1.5">
              {data.model && (
                <span
                  className="hidden max-w-[130px] truncate rounded border border-line bg-surface px-1.5 py-px font-mono text-[10.5px] text-fg-muted @xs:inline-block"
                  title={limit ? `${data.model} · ${data.windowDescription}` : data.model}
                >
                  {data.model}
                </span>
              )}
              {(focusPart || limit) && <span
                title={heroPillTitle}
                className={`rounded-full border px-2 py-px font-mono text-[11px] font-medium tabular-nums ${heroPillClass}`}
              >
                {heroPill}
              </span>}
            </div>
          </div>

          <div
            className="ctx-matrix-wrap mt-2.5 rounded-lg border border-line bg-surface p-2.5"
            onPointerLeave={() => setHovered(null)}
          >
            <div className="ctx-matrix" aria-hidden="true">
              {cells.map((part, index) => {
                const isFree = Boolean(part.free);
                const isActive = !isFree && activeKey === part.key;
                const isDim = Boolean(activeKey) && activeKey !== part.key && !isFree;
                return (
                  <i
                    key={index}
                    className="ctx-cell"
                    data-free={isFree ? 'true' : undefined}
                    data-active={isActive ? 'true' : undefined}
                    data-dim={isDim ? 'true' : undefined}
                    style={{ '--tile': part.color }}
                    onPointerEnter={() => setHovered(part.key)}
                    onClick={() => toggleSelect(part.key)}
                  />
                );
              })}
            </div>
          </div>

          <div className="mt-2 flex items-center justify-between gap-2 text-[11.5px]">
            <span className={`min-w-0 truncate tabular-nums ${!focusPart && ratio >= WARN_RATIO ? occupancyTextTone(ratio) : 'text-fg-muted'}`}>
              {footerCaption}
            </span>
            <span className="flex shrink-0 items-center gap-2">
              {focusPart ? (
                <span className="font-mono text-[11px] tabular-nums text-fg-body">
                  {formatExact(focusPart.tokens)} tokens
                </span>
              ) : (
                <span
                  className="text-[11px] text-fg-subtle"
                  title={counted ? '输入总量取自上游 usage，分类按本次输入和可关联的父响应摘要估算' : '按已记录的输入估算，尚无上游总量'}
                >
                  {counted ? '总量精确 · 分类估算' : (data.partial ? '已记录输入估算' : '输入用量估算')}
                </span>
              )}
            </span>
          </div>
        </section>

        {/* 中栏：输入分类占比列表 */}
        <section className="custom-scrollbar min-w-0 bg-inspector px-4 py-3 @3xl:min-h-0 @3xl:overflow-y-auto">
          <div className="mb-1.5 flex items-center justify-between gap-2">
            <h3 className="text-[11.5px] font-semibold tracking-[0.02em] text-fg-body">输入分类</h3>
            {selectedGroup ? (
              <button
                type="button"
                onClick={() => { setSelected(null); setHovered(null); }}
                className="text-[11px] font-medium text-accent hover:text-accent-fg"
              >
                清除选中
              </button>
            ) : (
              <span className="text-[11px] text-fg-subtle">Token / 占输入</span>
            )}
          </div>
          <div className="space-y-0.5" onPointerLeave={() => setHovered(null)}>
            {groups.map((group) => {
              const isSelected = selected === group.key;
              const isDim = Boolean(activeKey) && activeKey !== group.key && activeKey !== 'free';
              return (
                <button
                  key={group.key}
                  type="button"
                  aria-pressed={isSelected}
                  title={group.key === 'unassigned' ? data.attributionNote : undefined}
                  onPointerEnter={() => setHovered(group.key)}
                  onFocus={() => setHovered(group.key)}
                  onBlur={() => setHovered(null)}
                  onClick={() => toggleSelect(group.key)}
                  className={`flex h-[26px] w-full items-center gap-2 rounded-md px-2 text-xs transition-all ${
                    isSelected
                      ? 'bg-accent-soft text-accent-fg ring-1 ring-inset ring-accent-line/70'
                      : 'text-fg-body hover:bg-surface-mut'
                  } ${isDim ? 'opacity-45' : ''}`}
                >
                  <span className="size-2 shrink-0 rounded-[2px]" style={{ background: group.color }} />
                  <span className="truncate font-medium">{group.name}</span>
                  <span className="shrink-0 font-mono text-[10.5px] text-fg-subtle">{group.items.length}</span>
                  <span className="ml-auto shrink-0 font-mono text-[12px] tabular-nums text-fg">
                    {formatExact(group.tokens)}
                  </span>
                  <span className="w-11 shrink-0 text-right font-mono text-[11px] tabular-nums text-fg-muted">
                    {formatPercent(group.tokens, total)}
                  </span>
                </button>
              );
            })}
          </div>
        </section>

        {/* 右栏：高占用条目 / 选中分类全部条目 */}
        <section className="custom-scrollbar min-w-0 bg-inspector px-4 py-3 @xl:col-span-2 @3xl:col-span-1 @3xl:min-h-0 @3xl:overflow-y-auto">
          <div className="mb-1.5 flex items-center justify-between gap-2">
            <h3 className="flex min-w-0 items-center gap-1.5 text-[11.5px] font-semibold tracking-[0.02em] text-fg-body">
              {selectedGroup && (
                <span className="size-2 shrink-0 rounded-[2px]" style={{ background: selectedGroup.color }} />
              )}
              <span className="truncate">
                {selectedGroup ? `${selectedGroup.name}明细` : '高占用条目'}
              </span>
              <span className="shrink-0 font-mono text-[11px] font-normal text-fg-subtle">
                {visibleItems.length} 项
              </span>
            </h3>
            {selectedGroup ? (
              <button
                type="button"
                onClick={() => { setSelected(null); setHovered(null); }}
                className="shrink-0 text-[11px] font-medium text-accent hover:text-accent-fg"
              >
                查看全部
              </button>
            ) : (
              <span className="shrink-0 text-[11px] text-fg-subtle">按占用排序</span>
            )}
          </div>

          <div className="space-y-1" onPointerLeave={() => setHovered(null)}>
            {visibleItems.map((item) => {
              const widthPct = Math.max(4, (item.tokens / maxItemTokens) * 100);
              const isDim = Boolean(hovered) && hovered !== item.group.key && hovered !== 'free';
              const parentNote = item.historyTokens > 0
                ? `${item.group.name} · ${item.rawName}\n本次输入估算 ${formatExact(item.tokens - item.historyTokens)}，父响应摘要估算 ${formatExact(item.historyTokens)} tokens。` : null;
              const echoNote = item.echoTokens > 0
                ? `${item.rawName} · 本次上游响应回显的工具定义，按回显内容估算 ${formatExact(item.echoTokens)} tokens。` : null;
              const note = item.group.key === 'unassigned' ? data.attributionNote : (echoNote || parentNote);
              return (
                <div
                  key={`${item.group.key}:${item.rawName ?? item.name}`}
                  onPointerEnter={() => setHovered(item.group.key)}
                  title={note || `${item.group.name} · ${item.rawName ?? item.name} · ${formatExact(item.tokens)} tokens (${formatPercent(item.tokens, total)})`}
                  aria-description={note || undefined}
                  tabIndex={note ? 0 : undefined}
                  className={`relative flex h-[26px] items-center gap-2 overflow-hidden rounded px-2 text-xs transition-opacity ${isDim ? 'opacity-45' : ''}`}
                >
                  <span
                    aria-hidden="true"
                    className="ctx-item-fill pointer-events-none absolute inset-y-0 left-0 rounded"
                    style={{ '--tile': item.group.color, width: `${widthPct}%` }}
                  />
                  <span
                    className="relative z-10 size-1.5 shrink-0 rounded-[1.5px]"
                    style={{ background: item.group.color }}
                  />
                  <span className="relative z-10 min-w-0 flex-1 truncate font-mono text-[11.5px] text-fg-body">
                    {item.name}
                  </span>
                  <span className="relative z-10 shrink-0 font-mono text-[11.5px] tabular-nums text-fg">
                    {formatExact(item.tokens)}
                  </span>
                  <span className="relative z-10 w-10 shrink-0 text-right font-mono text-[10.5px] tabular-nums text-fg-muted">
                    {formatPercent(item.tokens, total)}
                  </span>
                </div>
              );
            })}
          </div>
        </section>
      </div>
    </div>
  );
}
