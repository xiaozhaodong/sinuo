// ============================================
// RequestStatusBadge - 请求状态徽章
// 2025-12-01 09:54:42
// ============================================

import {
  Clock,
  ArrowRightCircle,
  Settings,
  RotateCw,
  Pause,
  CheckCircle2,
  XCircle,
  Ban,
  Timer
} from 'lucide-react';
import { getStatusConfig } from '@utils/constants.js';

// 图标映射表
const ICON_MAP = {
  Clock,
  ArrowRightCircle,
  Settings,
  RotateCw,
  Pause,
  CheckCircle2,
  XCircle,
  Ban,
  Timer
};

// 进行中状态的图标动效：让「这条还在跑」在徽章上也读得出来。
// 终态（completed / failed / cancelled / timeout）一律静止。
//
// 与列表的「状态图标一律静止」不冲突：那条规则的前提是轨道正在动
// （见 index.css 的实时列表动效段），不叠第二个指示器。本组件现在只用在
// 详情面板的标题行，那里没有轨道 —— LifecyclePanel 唯一的 animate-pulse 是加载
// 骨架，不是进行中标识。所以这个图标是面板里唯一表达「还在跑」的东西，
// 停掉它 processing 就只剩一行静止文字，与已完成再无区别。
// 列表要静止还因为二十行一起转是噪音，面板一次只显示一条，不存在这个问题。
//
// motion-reduce 逐条附带：这里是 Tailwind utilities，不受 index.css 里
// 那个只覆盖自定义 class 的 @media 块管辖。
const ICON_MOTION_CLASS = {
  pending: 'animate-pulse motion-reduce:animate-none',
  forwarding: 'animate-pulse motion-reduce:animate-none',
  processing: 'animate-spin [animation-duration:2.6s] motion-reduce:animate-none',
  retry: 'animate-spin motion-reduce:animate-none'
};

/**
 * RequestStatusBadge - 请求状态徽章组件
 * @param {Object} props
 * @param {string} props.status - 状态值
 */
const RequestStatusBadge = ({ status }) => {
  const config = getStatusConfig(status);
  const IconComponent = ICON_MAP[config.icon];
  const motionClass = ICON_MOTION_CLASS[status] || '';

  return (
    <div className={`inline-flex items-center gap-1.5 px-2 py-0.5 rounded text-xs font-medium border ${config.color}`}>
      {IconComponent && <IconComponent className={`w-3 h-3 ${motionClass}`} />}
      {config.label}
    </div>
  );
};

export default RequestStatusBadge;
