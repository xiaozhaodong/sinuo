// 请求追踪模型标签颜色规则
// tone-* 一个类给齐底 / 字 / 边，深浅双主题由 index.css 统一维护。

const DEFAULT_MODEL_CLASSES = 'tone-slate';

const MODEL_COLOR_RULES = [
  {
    matches: (modelName) => modelName.includes('deepseek'),
    classes: 'tone-cyan'
  },
  {
    matches: (modelName) => modelName.includes('kimi') || modelName.includes('moonshot'),
    classes: 'tone-pink'
  },
  {
    matches: (modelName) => modelName.includes('glm-5.2') || modelName.startsWith('glm'),
    classes: 'tone-teal'
  },
  {
    matches: (modelName) => modelName.includes('fable-5') || modelName.includes('claude-fable-5'),
    classes: 'tone-rose'
  },
  {
    matches: (modelName) => modelName.includes('sonnet-5') || modelName.includes('claude-sonnet-5'),
    classes: 'tone-amber'
  },
  {
    matches: (modelName) => modelName.includes('sonnet-4') || modelName.includes('claude-sonnet-4'),
    classes: 'tone-orange'
  },
  {
    matches: (modelName) => modelName.includes('3-5-haiku') || modelName.includes('haiku'),
    classes: 'tone-green'
  },
  {
    matches: (modelName) => modelName.includes('3-5-sonnet') || (modelName.includes('sonnet') && modelName.includes('3.5')),
    classes: 'tone-blue'
  },
  {
    matches: (modelName) => modelName.includes('opus'),
    classes: 'tone-purple'
  },
  {
    matches: (modelName) => modelName === 'gpt-5.6' || modelName.startsWith('gpt-5.6-sol'),
    classes: 'tone-yellow'
  },
  {
    matches: (modelName) => modelName.startsWith('gpt-5.6-terra'),
    classes: 'tone-lime'
  },
  {
    matches: (modelName) => modelName.startsWith('gpt-5.6-luna'),
    classes: 'tone-sky'
  },
  {
    matches: (modelName) => modelName.includes('gpt-6-astra'),
    classes: 'tone-fuchsia'
  },
  {
    matches: (modelName) => modelName.includes('gpt-'),
    classes: 'tone-indigo'
  }
];

/**
 * 根据模型名称获取对应的颜色类名
 */
export const getModelColorClasses = (modelName) => {
  if (!modelName || modelName === 'unknown' || modelName === '-') {
    return DEFAULT_MODEL_CLASSES;
  }

  const lowerName = String(modelName).trim().toLowerCase();
  if (!lowerName || lowerName === 'unknown' || lowerName === '-') {
    return DEFAULT_MODEL_CLASSES;
  }

  const matchedRule = MODEL_COLOR_RULES.find((rule) => rule.matches(lowerName));

  return matchedRule?.classes ?? DEFAULT_MODEL_CLASSES;
};
