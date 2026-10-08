// 隐私规则测试抽屉：共用外壳负责模态生命周期。
import SidePanel from '@components/desktop/SidePanel.jsx';
import PrivacyRuleTestPanel from './PrivacyRuleTestPanel.jsx';

const PrivacyRuleTestDrawer = ({ open, onClose, onTest }) => (
  <SidePanel
    open={open}
    onClose={onClose}
    label="规则测试"
    title="规则测试"
    subtitle="测试文本不会被记录"
    width={620}
    layer={46}
    closeOnOverlay={true}
    overlayLabel="关闭规则测试"
  >
    <PrivacyRuleTestPanel onTest={onTest} variant="drawer" showHeader={false} />
  </SidePanel>
);

export default PrivacyRuleTestDrawer;
