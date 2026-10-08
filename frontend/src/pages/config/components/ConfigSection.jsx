// ConfigSection - 配置区块组件
import { FormSection } from '@components/desktop/FormSection.jsx';

const ConfigSection = ({ title, icon: Icon, children }) => (
  <div className="[&_.min-h-11>div:last-child]:min-w-0 [&_.min-h-11>div:last-child]:max-w-[65%] [&_.min-h-11>div:last-child]:shrink">
    <FormSection
      title={title}
      actions={Icon && <Icon size={14} className="text-fg-muted" aria-hidden="true" />}
    >
      {children}
    </FormSection>
  </div>
);

export default ConfigSection;
