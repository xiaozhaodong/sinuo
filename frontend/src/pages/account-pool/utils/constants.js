import { createSoftFailurePolicyState } from '../../../utils/softFailurePolicy.js';

// ============================================
// Account Pool 常量与样式映射
// 2026-03-07
// ============================================

const DEFAULT_BASE_URL = 'https://api.openai.com';

const AUTH_METHOD_OPTIONS = [
  {
    value: 'chatgpt_refresh_token',
    label: 'ChatGPT RT',
    description: 'ChatGPT 账号授权（使用 Refresh Token / rt）'
  },
  {
    value: 'api_key',
    label: 'API Key',
    description: 'OpenAI Responses API Key'
  }
];

const ACCOUNT_GROUP_OPTIONS = [
  {
    value: 'primary',
    label: '主组',
    description: '默认优先尝试这组账号'
  },
  {
    value: 'backup',
    label: '备组',
    description: '主组不可用时，再尝试这组账号'
  },
  {
    value: 'cold',
    label: '冷备',
    description: '主组和备组都不可用时，最后尝试'
  }
];

const ACCOUNT_STATE_STYLE = {
  active: 'tone-emerald',
  cooldown: 'tone-amber',
  disabled_auth: 'tone-rose',
  disabled: 'tone-slate'
};

const QUOTA_STATUS_STYLE = {
  ok: 'tone-sky',
  unavailable: 'tone-slate',
  exhausted: 'tone-amber',
  workspace_deactivated: 'tone-rose',
  auth_invalid: 'tone-rose',
  pending: 'tone-slate'
};

const SCHEDULE_OUTCOME_STYLE = {
  pending: 'tone-slate',
  success: 'tone-emerald',
  auth_failed: 'tone-rose',
  transient_failure: 'tone-amber',
  invalid_request: 'tone-amber',
  passthrough_no_available_providers: 'tone-sky',
  passthrough_other_4xx: 'tone-slate',
  no_schedulable_accounts: 'tone-slate'
};

const SCHEDULE_DECISION_STYLE = {
  selected: 'tone-indigo',
  eligible: 'tone-emerald',
  skipped: 'tone-slate'
};

const PLAN_TYPE_LABELS = {
  free: 'Free',
  plus: 'Plus',
  team: 'Team',
  enterprise: 'Enterprise',
  prepaid: 'Prepaid',
  unknown: 'Unknown'
};

const EMPTY_ACCOUNT_FORM = {
  ...createSoftFailurePolicyState(),
  account_name: '',
  auth_method: 'chatgpt_refresh_token',
  provider_type: 'chatgpt_refresh_token',
  group_key: 'primary',
  costMultiplier: '1.0',
  inputCostMultiplier: '1.0',
  outputCostMultiplier: '1.0',
  cacheCreationCostMultiplier: '1.0',
  cacheCreationCostMultiplier1h: '1.0',
  cacheReadCostMultiplier: '1.0',
  enableRequestCompression: false,
  stripCodexInternalMetadata: false,
  modelRewriteEnabled: false,
  modelRewriteRules: [
    { source: 'gpt-5.4', target: 'gpt-5.5' },
    { source: 'gpt-5.4-mini', target: 'gpt-5.5' }
  ],
  priority: '10',
  enabled: true,
  credential_raw: '',
  base_url: DEFAULT_BASE_URL
};

const MANUAL_FAILOVER_TIER_PRESETS = [
  {
    label: '主组',
    className: 'tone-indigo',
    description: '当前请求优先尝试这一层账号'
  },
  {
    label: '备组',
    className: 'tone-cyan',
    description: '主组全部失败后切到这一层'
  },
  {
    label: '冷备',
    className: 'tone-violet',
    description: '主组和备组都不可用时，再切到这一组'
  }
];

export {
  ACCOUNT_GROUP_OPTIONS,
  ACCOUNT_STATE_STYLE,
  AUTH_METHOD_OPTIONS,
  DEFAULT_BASE_URL,
  EMPTY_ACCOUNT_FORM,
  MANUAL_FAILOVER_TIER_PRESETS,
  PLAN_TYPE_LABELS,
  QUOTA_STATUS_STYLE,
  SCHEDULE_DECISION_STYLE,
  SCHEDULE_OUTCOME_STYLE
};
