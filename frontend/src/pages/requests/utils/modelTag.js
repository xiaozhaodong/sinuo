// 请求追踪模型标签颜色规则
// 已识别模型用 index.css 的 grad-tone + model-tone-* 渐变（底 / 描边 / 字，深浅双主题）；未知模型用 tone-slate 纯色。

const DEFAULT_MODEL_CLASSES = 'tone-slate';

const modelTone = (key) => `grad-tone model-tone-${key}`;

const MODEL_COLOR_RULES = [
  {
    matches: (modelName) => modelName.includes('deepseek'),
    classes: modelTone('deepseek')
  },
  {
    matches: (modelName) => modelName.includes('kimi') || modelName.includes('moonshot'),
    classes: modelTone('kimi')
  },
  {
    matches: (modelName) => modelName.includes('glm-5.2') || modelName.startsWith('glm'),
    classes: modelTone('glm')
  },
  {
    matches: (modelName) => modelName.includes('fable-5') || modelName.includes('claude-fable-5'),
    classes: modelTone('fable')
  },
  {
    matches: (modelName) => modelName.includes('sonnet-5') || modelName.includes('claude-sonnet-5'),
    classes: modelTone('sonnet-5')
  },
  {
    matches: (modelName) => modelName.includes('sonnet-4') || modelName.includes('claude-sonnet-4'),
    classes: modelTone('sonnet-4')
  },
  {
    matches: (modelName) => modelName.includes('3-5-haiku') || modelName.includes('haiku'),
    classes: modelTone('haiku')
  },
  {
    matches: (modelName) => modelName.includes('3-5-sonnet') || (modelName.includes('sonnet') && modelName.includes('3.5')),
    classes: modelTone('sonnet-3-5')
  },
  {
    matches: (modelName) => modelName.includes('opus'),
    classes: modelTone('opus')
  },
  {
    matches: (modelName) => modelName === 'gpt-5.6' || modelName.startsWith('gpt-5.6-sol') || modelName.includes('gpt-6-sol'),
    classes: modelTone('sol')
  },
  {
    matches: (modelName) => modelName.startsWith('gpt-5.6-terra'),
    classes: modelTone('terra')
  },
  {
    matches: (modelName) => modelName.startsWith('gpt-5.6-luna') || modelName.includes('gpt-6-luna'),
    classes: modelTone('luna')
  },
  {
    matches: (modelName) => modelName.includes('gpt-6-astra'),
    classes: modelTone('astra')
  },
  {
    matches: (modelName) => modelName.includes('gpt-'),
    classes: modelTone('gpt')
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
