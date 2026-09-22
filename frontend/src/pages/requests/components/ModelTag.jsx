// ============================================
// ModelTag - 模型标签组件（带颜色区分）
// 2025-12-01 09:30:08
// ============================================

import React from 'react';
import { createPortal } from 'react-dom';
import { AlertTriangle } from 'lucide-react';
import { getModelColorClasses } from '../utils/modelTag.js';

const ModelTooltip = ({ anchorRect, children, tooltipId }) => {
  if (!anchorRect) {
    return null;
  }

  const viewportWidth = window.innerWidth;
  const centerX = Math.min(
    Math.max(anchorRect.left + anchorRect.width / 2, 192),
    viewportWidth - 192
  );

  return createPortal(
    <span
      id={tooltipId}
      role="tooltip"
      className="pointer-events-none fixed z-[10001] max-w-[360px] -translate-x-1/2 break-all rounded-lg border border-line bg-inverted px-3 py-2 text-xs font-mono leading-5 whitespace-normal text-fg-inverted shadow-lg"
      style={{ left: `${centerX}px`, top: `${anchorRect.bottom + 6}px` }}
    >
      {children}
    </span>,
    document.body
  );
};

/**
 * ModelTag - 显示模型名称的标签组件
 * @param {Object} props
 * @param {string} props.model - 模型名称
 * @param {boolean} props.compact - 是否限制宽度并省略尾部内容
 * @param {{ level: string, lines: string[] } | null} [props.alert] - 模型一致性告警（仅 compact 生效；
 *   level === 'mismatch' 时在名称右侧显示警告图标，tooltip 逐行显示 lines）
 */
const ModelTag = ({ model, compact = false, alert = null }) => {
  const [tooltipAnchor, setTooltipAnchor] = React.useState(null);
  const [isOverflowing, setIsOverflowing] = React.useState(false);
  const textRef = React.useRef(null);
  const tooltipId = React.useId();
  const displayName = (!model || model === 'unknown') ? '-' : model;
  // 非 compact（详情弹窗）不显示 alert，由卡片自己表达
  const isMismatch = compact && alert?.level === 'mismatch';
  const colorClasses = getModelColorClasses(model);
  const alertLines = isMismatch ? (alert.lines || []) : [];
  const hasTooltip = compact && ((displayName !== '-' && isOverflowing) || alertLines.length > 0);

  React.useLayoutEffect(() => {
    const textElement = textRef.current;
    if (!compact || !textElement) {
      setIsOverflowing(false);
      return undefined;
    }

    const updateOverflowState = () => {
      setIsOverflowing(textElement.scrollWidth > textElement.clientWidth);
    };

    updateOverflowState();

    const resizeObserver = typeof ResizeObserver === 'undefined'
      ? null
      : new ResizeObserver(updateOverflowState);
    resizeObserver?.observe(textElement);
    window.addEventListener('resize', updateOverflowState);

    return () => {
      resizeObserver?.disconnect();
      window.removeEventListener('resize', updateOverflowState);
    };
  }, [compact, displayName]);

  const showTooltip = (event) => {
    if (hasTooltip) {
      setTooltipAnchor(event.currentTarget.getBoundingClientRect());
    }
  };

  const hideTooltip = () => {
    setTooltipAnchor(null);
  };

  return (
    <>
      <span
        className={`inline-flex min-w-0 items-center gap-1 rounded border px-2 py-1 align-middle text-xs font-mono transition-all ${compact ? 'max-w-[160px] hover:shadow-sm focus:outline-none focus:ring-2 focus:ring-accent-soft' : ''} ${colorClasses}`}
        tabIndex={hasTooltip ? 0 : undefined}
        data-model-alert={isMismatch ? 'mismatch' : undefined}
        aria-describedby={tooltipAnchor ? tooltipId : undefined}
        onMouseEnter={showTooltip}
        onMouseLeave={hideTooltip}
        onFocus={showTooltip}
        onBlur={hideTooltip}
      >
        <span ref={textRef} className={compact ? 'min-w-0 truncate' : ''}>{displayName}</span>
        {isMismatch && <AlertTriangle className="h-3 w-3 flex-shrink-0 text-warn" aria-label="模型或思考等级与请求不一致" />}
      </span>
      {hasTooltip && (
        <ModelTooltip anchorRect={tooltipAnchor} tooltipId={tooltipId}>
          {alertLines.length > 0
            ? alertLines.map((line, index) => <span key={index} className="block whitespace-pre">{line}</span>)
            : displayName}
        </ModelTooltip>
      )}
    </>
  );
};

export default ModelTag;
