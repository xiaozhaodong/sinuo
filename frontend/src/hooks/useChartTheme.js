// ============================================
// 图表色板 —— recharts 的 stroke / fill / contentStyle 是 JS props，
// 读不到 CSS 变量，因此主题色板必须走 JS 而非 index.css 的 token 层。
// 键名按语义定，不按色值定：同一 hex 在不同图表里语义不同，暗色可分别调。
// ============================================

import { useTheme } from '@contexts/ThemeContext.jsx';

const LIGHT_CHART_THEME = {
  grid: '#f1f5f9',
  axis: '#94a3b8',
  axisStrong: '#64748b',
  cursor: '#f8fafc',
  // activeDot 的描边色，需与图表卡片底色一致
  dotStroke: '#ffffff',
  tooltip: {
    contentStyle: {
      borderRadius: '8px',
      border: '1px solid #e2e8f0',
      backgroundColor: '#ffffff',
      boxShadow: '0 4px 12px rgb(0 0 0 / 0.1)',
      color: '#0f172a'
    },
    labelStyle: { color: '#0f172a' },
    itemStyle: { color: '#334155' }
  },
  series: {
    // 请求趋势
    total: '#6366f1',
    success: '#10b981',
    fail: '#f43f5e',
    trendOther: '#cbd5e1',
    // Token 成本
    tokens: '#818cf8',
    cost: '#f43f5e',
    // 响应时间
    avg: '#06b6d4',
    min: '#10b981',
    max: '#f43f5e',
    // 连接活动
    connections: '#8b5cf6',
    // Token 分布
    input: '#6366f1',
    output: '#10b981',
    cacheCreation: '#f59e0b',
    // 缓存读取占比最大但最便宜，使用中性灰避免抢主视觉。
    cacheRead: '#94a3b8',
    // 端点连通性
    healthy: '#10b981',
    unhealthy: '#ef4444'
  }
};

const DARK_CHART_THEME = {
  grid: '#1e293b',
  axis: '#64748b',
  axisStrong: '#94a3b8',
  cursor: '#1e293b',
  dotStroke: '#0f172a',
  tooltip: {
    contentStyle: {
      borderRadius: '8px',
      border: '1px solid #2c3a4f',
      backgroundColor: '#0f172a',
      boxShadow: '0 8px 24px rgb(0 0 0 / 0.5)',
      color: '#e2e8f0'
    },
    labelStyle: { color: '#e2e8f0' },
    itemStyle: { color: '#cbd5e1' }
  },
  series: {
    total: '#818cf8',
    success: '#34d399',
    fail: '#fb7185',
    trendOther: '#475569',
    tokens: '#a5b4fc',
    cost: '#fb7185',
    avg: '#22d3ee',
    min: '#34d399',
    max: '#fb7185',
    connections: '#a78bfa',
    input: '#818cf8',
    output: '#34d399',
    cacheCreation: '#fbbf24',
    // 缓存读取占比最大但最便宜，使用中性灰避免抢主视觉。
    cacheRead: '#64748b',
    healthy: '#34d399',
    unhealthy: '#f87171'
  }
};

// 两套色板都是模块级常量，引用天然稳定，无需 useMemo
const useChartTheme = () => {
  const { resolved } = useTheme();
  return resolved === 'dark' ? DARK_CHART_THEME : LIGHT_CHART_THEME;
};

export default useChartTheme;
