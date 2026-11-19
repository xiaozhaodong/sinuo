import { initWails, getWailsMethod } from '@utils/wailsApi.js';

const call = async (name, ...args) => {
  if (!(await initWails())) throw new Error('请在 Sinuo 桌面端使用计费配置');
  return getWailsMethod(name)(...args);
};

export const getRateEditor = (model) => call('GetModelRateSheetEditor', model);
export const publishRateSheet = (model, source, sheet) => call('PublishModelRateSheet', model, source, sheet);
export const getChannelRule = (channel, model) => call('GetChannelModelRule', channel, model);
export const getChannelRules = (channel) => call('GetChannelModelRules', channel);
export const saveChannelRule = (channel, model, rule) => call('SaveChannelModelRule', channel, model, rule);
export const getOfficialCost = (id) => call('GetRequestOfficialCost', id);
