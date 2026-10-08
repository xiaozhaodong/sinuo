// ============================================
// 桌面化组件 - 分组表单（系统设置式）
// 2026-10-07
// ============================================
// 浅灰底上的白色分组框：每行左侧是名称与说明，右侧是控件。
// 设置页、客户端接入页共用；页面外层负责浅灰底（bg-pane）与居中限宽。

import { Children } from 'react';

export const FormSection = ({ id, title, description, actions = null, children }) => (
  <section id={id} className="scroll-mt-4">
    <div className="mx-1 mb-1.5 mt-5 flex items-baseline gap-2">
      <h2 className="shrink-0 text-[13px] font-semibold text-fg">{title}</h2>
      {description && <span className="min-w-0 truncate text-xs text-fg-muted" title={description}>{description}</span>}
      {actions && <span className="ml-auto flex shrink-0 self-center items-center gap-1.5">{actions}</span>}
    </div>
    {Children.toArray(children).length > 0 && (
      <div className="divide-y divide-line-soft rounded-[10px] border border-line bg-surface">{children}</div>
    )}
  </section>
);

export const FormRow = ({ label, description, badge = null, children }) => (
  <div className="flex min-h-11 items-center gap-4 px-3.5 py-2">
    <div className="min-w-0 flex-1">
      <div className="flex items-center gap-1.5 text-[13px] text-fg">{label}{badge}</div>
      {description && <div className="mt-0.5 text-[11.5px] leading-snug text-fg-muted">{description}</div>}
    </div>
    <div className="flex shrink-0 items-center gap-2">{children}</div>
  </div>
);

// 整行自由内容（额度条、代码片段等不适合左右分栏的内容）
export const FormBlock = ({ children }) => <div className="px-3.5 py-3">{children}</div>;

// 编辑表单字段：右对齐标签，控件下方保留说明与校验错误。
export const FormFieldRow = ({ label, htmlFor, required = false, hint, error, children }) => (
  <div className="flex items-start gap-3 px-3.5 py-2">
    <label htmlFor={htmlFor} className="w-[112px] shrink-0 pt-[3px] text-right text-[12.5px] text-fg-body">
      {label}{required && <span className="ml-0.5 text-danger">*</span>}
    </label>
    <div className="min-w-0 flex-1">
      {children}
      {hint && <div className="mt-1 text-[11.5px] leading-snug text-fg-muted">{hint}</div>}
      {error && <div className="mt-1 text-[11.5px] text-danger">{error}</div>}
    </div>
  </div>
);
