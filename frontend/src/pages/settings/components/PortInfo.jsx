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

  const {
    preferred_port,
    actual_port,
    was_occupied,
    lan_access = false,
    lan_access_active = false,
    lan_token_configured = false,
    lan_addresses = []
  } = portInfo;

  const scopeLabel = lan_access_active ? '局域网' : '仅本机';
  const lanAddresses = Array.isArray(lan_addresses) ? lan_addresses : [];
  const restartHint = lan_access === lan_access_active
    ? ''
    : lan_access
      ? '已保存局域网访问设置，重启后生效。'
      : '已拒绝其他设备，重启后只监听本机。';
  const portDescription = [
    was_occupied
      ? `首选端口 ${preferred_port} 已被其他程序占用，已自动切换。如需使用 ${preferred_port}，请关闭占用它的程序后重启应用。`
      : '与首选端口一致',
    restartHint
  ].filter(Boolean).join(' ');

  return (
    <>
      <FormRow
        label={`当前监听端口 · ${scopeLabel}`}
        description={portDescription}
      >
        <span className="flex items-center gap-1.5">
          <span className={`h-[7px] w-[7px] rounded-full ${was_occupied ? 'bg-warn-solid' : 'bg-success-solid'}`} />
          <span className="font-mono text-[13px] text-fg">{actual_port}</span>
        </span>
      </FormRow>
      {lan_access_active && (
        <FormRow label="局域网地址" description="其他设备访问主端口时使用以下地址，并携带访问 Token">
          <div className="select-text space-y-0.5 text-right font-mono text-[12px] text-fg">
            {lanAddresses.length > 0 ? lanAddresses.map((address) => <div key={address}>{address}</div>) : '未发现局域网地址'}
          </div>
        </FormRow>
      )}
      {lan_access_active && !lan_token_configured && (
        <div className="mx-3.5 mb-2 flex items-start gap-2 rounded-md border border-warn-line bg-warn-soft px-3 py-2 text-xs text-fg">
          <span className="h-[7px] w-[7px] shrink-0 rounded-full bg-warn-solid" />
          <span>未设置访问 Token，其他设备的请求会全部被拒绝。</span>
        </div>
      )}
    </>
  );
};

export default PortInfo;
