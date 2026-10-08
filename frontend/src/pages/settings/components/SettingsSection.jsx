// ============================================
// SettingsSection - 设置分类区块组件
// v5.1.0 (2025-12-08)
// 2026-10-07：桌面化——系统设置式分组框，「恢复默认」放在组标题右侧
// ============================================

import { FormSection } from '@components/desktop/FormSection.jsx';

const SettingsSection = ({
  id,
  title,
  description,
  children,
  onReset,
  resetDisabled = false
}) => (
  <FormSection
    id={id}
    title={title}
    description={description}
    actions={onReset && (
      <button
        type="button"
        onClick={onReset}
        disabled={resetDisabled}
        title="把本组设置恢复为默认值"
        className="text-xs text-fg-muted hover:text-fg disabled:opacity-40"
      >
        恢复默认
      </button>
    )}
  >
    {children}
  </FormSection>
);

export default SettingsSection;
