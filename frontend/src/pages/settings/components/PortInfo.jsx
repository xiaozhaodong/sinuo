// ============================================
// PortInfo - 端口信息显示组件
// v5.1.0 (2025-12-08)
// 2026-10-07：桌面化——服务端口分组里的一行「当前监听端口」
// ============================================

import { FormRow } from '@components/desktop/FormSection.jsx';

const PortInfo = ({ portInfo, loading = false }) => {
  if (loading || !portInfo) {
    return <FormRow label="当前监听端口" description="读取中…" />;
  }

  const { preferred_port, actual_port, was_occupied } = portInfo;

  return (
    <FormRow
      label="当前监听端口"
      description={was_occupied
        ? `首选端口 ${preferred_port} 已被其他程序占用，已自动切换。如需使用 ${preferred_port}，请关闭占用它的程序后重启应用。`
        : '与首选端口一致'}
    >
      <span className="flex items-center gap-1.5">
        <span className={`h-[7px] w-[7px] rounded-full ${was_occupied ? 'bg-warn-solid' : 'bg-success-solid'}`} />
        <span className="font-mono text-[13px] text-fg">{actual_port}</span>
      </span>
    </FormRow>
  );
};

export default PortInfo;
