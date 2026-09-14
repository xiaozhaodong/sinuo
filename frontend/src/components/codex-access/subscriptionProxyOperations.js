// Codex 接入模式操作：后端配置是唯一事实来源，控制器只持有当前页面的读回快照。
import { resolveErrorMessage } from '../../utils/errors.js';
import { buildSubscriptionProxyNotice, isRevisionConflict, isSubscriptionProxyStoppedByUser, resolveSubscriptionProxyDisplayState } from './subscriptionProxyViewModel.js';

const MODES = new Set(['subscription', 'pool']);

export const readSubscriptionProxyState = async (api) => {
  const [config, status] = await Promise.allSettled([api.getConfig(), api.getStatus()]);
  return {
    config: config.status === 'fulfilled' ? config.value : null,
    status: status.status === 'fulfilled' ? status.value : null,
    configError: config.status === 'rejected' ? resolveErrorMessage(config.reason, '配置读取失败') : '',
    statusError: status.status === 'rejected' ? resolveErrorMessage(status.reason, '运行态读取失败') : ''
  };
};

export const modeSwitchBlockReason = ({ config, status, configError, statusError, fallbackRouteLabel = '' }) => {
  if (configError) return `配置读取失败：${configError}`;
  if (!config) return '尚未读到接入配置，请刷新。';
  if (!config.initialized || !config.accessConfigured || config.revision < 1) {
    return '请先到 Codex 管理 → 客户端接入完成初始化。';
  }
  if (statusError) return `运行态读取失败：${statusError}`;
  if (!status) return '尚未读到运行态，请刷新。';
  // 只有配置已停用且运行态已停稳，才是正常终态；停用中/停用失败仍要保留真实状态。
  if (isSubscriptionProxyStoppedByUser({ config, status })) {
    const route = fallbackRouteLabel ? `（原有入口策略：${fallbackRouteLabel}）` : '';
    return `独立入口已停用${route}。仍指向独立入口的 Codex 客户端将无法连接；如需使用原有入口，请先修改客户端接入配置。要重新使用独立入口，请到 Codex 管理 → 客户端接入启用。`;
  }
  if (!config.enabled && status.state === 'running') {
    return '停用意图已保存，但独立入口仍在运行；请刷新运行态确认停用完成。';
  }
  if (status.state !== 'running') {
    const { label } = resolveSubscriptionProxyDisplayState({ config, status });
    return `独立入口${label}，暂不能切换模式，请到 Codex 管理 → 客户端接入检查运行状态。`;
  }
  if (!config.enabled || !status.enabled || !MODES.has(config.mode) || config.mode !== status.mode || config.revision !== status.revision) {
    return '配置与运行态尚未一致，请刷新后重新选择。';
  }
  return '';
};

// 普通完整保存和快捷模式保存使用相同字段映射；不改变地址、启用意图或 WS 压缩。
export const subscriptionProxyConfigInput = (config, mode = config.mode) => ({
  enabled: config.enabled,
  mode,
  listen_host: config.listenHost,
  listen_port: config.listenPort,
  ws_compression: config.wsCompression
});

export const createSubscriptionProxyController = (api) => {
  let state = {
    config: null, status: null, configError: '', statusError: '',
    loading: true, busy: false, notice: null
  };
  let sequence = 0;
  const listeners = new Set();
  const update = (patch) => {
    state = { ...state, ...patch };
    listeners.forEach((listener) => listener());
  };
  const getSnapshot = () => state;
  const subscribe = (listener) => {
    listeners.add(listener);
    return () => listeners.delete(listener);
  };

  const refresh = async ({ silent = false } = {}) => {
    // 保存后的独立读回由操作自己完成；事件或手动刷新不能抢占它。
    if (state.busy) return state;
    const ticket = ++sequence;
    if (!silent) update({ loading: true });
    const next = await readSubscriptionProxyState(api);
    if (ticket === sequence) update({ ...next, loading: false });
    return state;
  };

  const switchMode = async (targetMode, expectedConfig) => {
    if (state.busy) return { saved: false, confirmed: false, notice: state.notice };
    // 让已发出的旧读取失效，避免它在新模式保存后把 UI 回写成旧模式。
    ++sequence;
    update({ busy: true, loading: false, notice: null });
    let savedConfig = null;
    try {
      if (!MODES.has(targetMode)) throw new Error('不支持的 Codex 接入模式。');
      if (!expectedConfig?.initialized || !Number.isInteger(expectedConfig.revision) || expectedConfig.revision < 1) {
        throw new Error('配置尚未初始化，请到 Codex 管理 → 客户端接入设置。');
      }

      const before = await readSubscriptionProxyState(api);
      update(before);
      const blocked = modeSwitchBlockReason(before);
      if (blocked) throw new Error(blocked);
      if (before.config.revision !== expectedConfig.revision) {
        throw new Error('Codex 接入配置已被其他写入更新，请刷新后重新确认。');
      }
      if (before.config.mode === targetMode) throw new Error('当前已经是所选模式，请刷新状态。');

      // 确认框固定 revision；即使保存前又有写入，后端 CAS 仍会拒绝旧版本。
      const result = await api.saveConfig(
        subscriptionProxyConfigInput(before.config, targetMode), expectedConfig.revision
      );
      savedConfig = result.config;
      const after = await readSubscriptionProxyState(api);
      const confirmed = !modeSwitchBlockReason(after)
        && after.config.mode === targetMode
        && after.config.revision === savedConfig.revision;
      let notice;
      if (confirmed) {
        notice = buildSubscriptionProxyNotice({
          intent: 'save', modeChanged: true, config: after.config, status: after.status
        });
      } else {
        const detail = after.configError || after.statusError || after.status?.lastError
          || '配置或运行态已变化';
        notice = {
          type: after.status?.state === 'failed' ? 'error' : 'info',
          text: `模式配置已保存，但尚未确认运行态：${detail}。请刷新状态，已有 WS 在重连后生效。`,
          action: 'refresh'
        };
      }
      update({ ...after, config: after.config || savedConfig, notice });
      return { saved: true, confirmed, config: state.config, status: state.status, notice };
    } catch (error) {
      const message = resolveErrorMessage(error, '切换模式失败');
      const notice = {
        type: 'error',
        text: isRevisionConflict(message) ? `${message}（不会自动覆盖，请重新选择。）` : message,
        action: 'refresh'
      };
      update({ notice });
      return { saved: savedConfig !== null, confirmed: false, config: state.config, status: state.status, notice };
    } finally {
      update({ busy: false });
    }
  };

  return {
    getSnapshot, subscribe, refresh, switchMode,
    clearNotice: () => update({ notice: null }),
    // 状态事件只用来提示重新读回，失败时保留手动刷新能力并明确告知。
    reportEventError: (error) => update({ notice: {
      type: 'info', text: `运行态自动更新不可用：${resolveErrorMessage(error)}。可手动刷新。`
    } }),
    invalidateReads: () => { ++sequence; }
  };
};
