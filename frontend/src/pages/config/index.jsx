// ============================================
// Config 页面 - 配置查看（增强版）
// 结合预设section和动态section，确保显示所有配置数据
// 2025-12-01
// ============================================

import { useState, useEffect, useCallback } from 'react';
import {
  Settings,
  RefreshCw,
  Server,
  Shield,
  Database,
  Globe
} from 'lucide-react';
import { Button, LoadingSpinner, ErrorMessage } from '@components/ui';
import { FormBlock } from '@components/desktop/FormSection.jsx';
import { fetchConfig } from '@utils/api.js';
import {
  ConfigSection,
  ConfigItem,
  DynamicConfigSection
} from './components';

// ============================================
// Config 页面主组件
// ============================================

const ConfigPage = () => {
  const [config, setConfig] = useState(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState(null);

  const loadConfig = useCallback(async () => {
    try {
      setLoading(true);
      setError(null);
      const data = await fetchConfig();
      setConfig(data);
    } catch (err) {
      setError(err.message);
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    loadConfig();
  }, [loadConfig]);

  if (error) {
    return (
      <ErrorMessage
        title="配置加载失败"
        message={error}
        onRetry={loadConfig}
      />
    );
  }

  if (loading) {
    return <LoadingSpinner text="加载配置..." />;
  }

  // 定义已预设的section列表（这些会使用特定的展示方式）
  const presetSections = new Set([
    'server',
    'web',
    'health',
    'request_suspend',
    'usage_tracking',
    'token_counting',
    'timezone',
    'global_timeout'
  ]);

  // 获取所有动态section（除了预设的和endpoints）
  const dynamicSections = config
    ? Object.keys(config).filter(
        key =>
          !presetSections.has(key) &&
          typeof config[key] === 'object' &&
          config[key] !== null &&
          !Array.isArray(config[key])
      )
    : [];

  // 获取顶层的简单配置项
  const topLevelItems = config
    ? Object.keys(config).filter(
        key =>
          !presetSections.has(key) &&
          typeof config[key] !== 'object'
      )
    : [];

  return (
    <div className="motion-page min-h-0 flex-1 overflow-y-auto bg-pane px-7 pb-12">
      <div className="mx-auto max-w-[640px]">
        <div className="mx-1 mt-5 flex items-center justify-between gap-3">
          <div>
            <h1 className="text-[13px] font-semibold text-fg">系统配置</h1>
            <p className="mt-1 text-xs text-fg-muted">查看当前系统配置参数（只读）</p>
          </div>
          <Button size="sm" variant="secondary" icon={RefreshCw} onClick={loadConfig}>刷新</Button>
        </div>

      {/* ========== 预设配置区块（两列布局）========== */}
      <div className="space-y-0">
        {/* 服务配置 */}
        {config?.server && (
          <ConfigSection title="服务配置" icon={Server}>
            <ConfigItem label="监听地址" value={config.server.Host} />
            <ConfigItem label="监听端口" value={config.server.Port} type="number" />
          </ConfigSection>
        )}

        {/* Web 界面配置 */}
        {config?.web && (
          <ConfigSection title="Web 界面" icon={Globe}>
            <ConfigItem label="启用状态" value={config.web.Enabled} type="boolean" />
            <ConfigItem label="监听地址" value={config.web.Host} />
            <ConfigItem label="监听端口" value={config.web.Port} type="number" />
          </ConfigSection>
        )}

        {/* 连通性测试配置 */}
        {config?.health && (
          <ConfigSection title="连通性测试" icon={Shield}>
            <ConfigItem
              label="轮询间隔"
              value={config.health.CheckInterval}
              type="duration"
            />
            <ConfigItem
              label="检测超时"
              value={config.health.Timeout}
              type="duration"
            />
            <ConfigItem
              label="检测路径"
              value={config.health.HealthPath || '直接访问端点 URL'}
            />
          </ConfigSection>
        )}

        {/* 使用追踪配置 */}
        {config?.usage_tracking && (
          <ConfigSection title="使用追踪" icon={Database}>
            <ConfigItem
              label="启用追踪"
              value={config.usage_tracking.Enabled}
              type="boolean"
            />
            <ConfigItem
              label="数据库类型"
              value={config.usage_tracking.Database?.Type}
            />
            <ConfigItem
              label="数据库路径"
              value={config.usage_tracking.Database?.Path || config.usage_tracking.DatabasePath}
            />
            <ConfigItem
              label="数据保留天数"
              value={config.usage_tracking.RetentionDays}
              type="number"
            />
            <ConfigItem
              label="缓冲区大小"
              value={config.usage_tracking.BufferSize}
              type="number"
            />
            <ConfigItem
              label="批量大小"
              value={config.usage_tracking.BatchSize}
              type="number"
            />
          </ConfigSection>
        )}
      </div>

      {/* ========== 顶层简单配置项 ========== */}
      {topLevelItems.length > 0 && (
        <ConfigSection title="全局配置" icon={Settings}>
          {topLevelItems.map(key => (
            <ConfigItem
              key={key}
              label={key}
              value={config[key]}
              type={
                typeof config[key] === 'boolean'
                  ? 'boolean'
                  : typeof config[key] === 'number'
                  ? 'number'
                  : /^\d+[smh]$/.test(String(config[key]))
                  ? 'duration'
                  : 'text'
              }
            />
          ))}
        </ConfigSection>
      )}

      {/* ========== 动态配置区块（单列布局）========== */}
      {dynamicSections.length > 0 && (
        <div className="space-y-0">
          {dynamicSections.map(sectionName => (
            <DynamicConfigSection
              key={sectionName}
              sectionName={sectionName}
              sectionData={config[sectionName]}
            />
          ))}
        </div>
      )}

      {/* ========== 原始配置 JSON ========== */}
      <ConfigSection title="原始配置 (JSON)" icon={Settings}>
        <FormBlock>
        <pre className="selectable max-h-96 overflow-auto rounded-md border border-line-soft bg-pane px-3 py-2 font-mono text-xs text-fg-body">
          {JSON.stringify(config, null, 2)}
        </pre>
        </FormBlock>
      </ConfigSection>
      </div>
    </div>
  );
};

export default ConfigPage;
