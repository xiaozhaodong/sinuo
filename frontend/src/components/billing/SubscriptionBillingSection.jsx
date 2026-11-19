import { FormRow, FormSection } from '@components/desktop/FormSection.jsx';
import ChannelRuleEditor from './ChannelRuleEditor.jsx';

// 客户端接入页签里的订阅直通计费入口：规则只作用于对应订阅通道。
const SubscriptionBillingSection = ({ name, label }) => (
  <FormSection title="计费">
    <FormRow label="按模型规则" description="只作用于订阅直通请求">
      <ChannelRuleEditor type="subscription" name={name} label={label} />
    </FormRow>
  </FormSection>
);

export default SubscriptionBillingSection;
