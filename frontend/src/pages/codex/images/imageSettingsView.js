export const IMAGE_GENERATION_CATEGORY = 'image_generation';

// 分组只决定摆放位置；每项的名称与说明仍以后端返回为准。
// 「上游」单独成组：以后支持多个图像上游时只替换这一组。
export const IMAGE_SETTING_GROUPS = [
  { id: 'takeover', title: '接管', description: '只作用于主端口；Codex 独立入口的图像请求直接透传 ChatGPT，不经过这里', keys: ['enabled'] },
  { id: 'upstream', title: '上游', description: '当前只支持一个 OpenAI 兼容上游', keys: ['endpoint_url', 'api_key', 'model', 'timeout'] },
  { id: 'billing', title: '计费', description: '只用于请求追踪里的成本记录', keys: ['fixed_price_usd'] },
  { id: 'network', title: '网络', description: '配合 Surge 等代理工具让图像请求直连', keys: ['direct_connect', 'direct_source_port_range'] }
];

export const groupImageSettings = (settings) => {
  const imageSettings = (settings || []).filter((setting) => setting.category === IMAGE_GENERATION_CATEGORY);
  const groupedKeys = new Set(IMAGE_SETTING_GROUPS.flatMap((group) => group.keys));
  const groups = IMAGE_SETTING_GROUPS.map(({ keys, ...group }) => ({
    ...group,
    items: keys.map((key) => imageSettings.find((setting) => setting.key === key)).filter(Boolean)
  })).filter((group) => group.items.length > 0);
  const otherItems = imageSettings.filter((setting) => !groupedKeys.has(setting.key))
    .sort((a, b) => a.display_order - b.display_order);
  if (otherItems.length > 0) {
    groups.push({ id: 'other', title: '其他', description: '', items: otherItems });
  }
  return groups;
};

export const applySettingChange = (changes, setting, value) => {
  const next = { ...changes };
  if (String(value) === String(setting.value)) {
    delete next[setting.key];
  } else {
    next[setting.key] = value;
  }
  return next;
};

export const buildImageSettingUpdates = (changes) => Object.entries(changes).map(([key, value]) => ({
  category: IMAGE_GENERATION_CATEGORY,
  key,
  value: String(value)
}));
