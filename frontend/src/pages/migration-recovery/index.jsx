import { useState } from 'react';
import { AlertTriangle, Archive, Database, FileCog, LoaderCircle, RefreshCw, ShieldCheck } from 'lucide-react';
import { retryStartupMigration } from '@utils/wailsApi.js';

const Detail = ({ icon: Icon, label, value }) => (
  <div className="rounded-xl border border-line bg-surface p-4">
    <div className="flex items-center gap-2 text-xs font-semibold uppercase tracking-[0.12em] text-fg-subtle"><Icon size={14} />{label}</div>
    <div className="mt-2 break-all font-mono text-xs leading-5 text-fg-body">{value || '未生成'}</div>
  </div>
);

const MigrationRecoveryPage = ({ status = {}, onStatusChange }) => {
  const [retrying, setRetrying] = useState(false);
  const [retryError, setRetryError] = useState('');
  const failed = status.state === 'migration_failed';

  const retry = async () => {
    setRetrying(true);
    setRetryError('');
    onStatusChange?.({ ...status, state: 'migrating', error: '', retryAllowed: false });
    try {
      const next = await retryStartupMigration();
      onStatusChange?.(next);
    } catch (error) {
      const message = error?.message || '重试失败';
      setRetryError(message);
      onStatusChange?.({ ...status, state: 'migration_failed', error: message, retryAllowed: true });
    } finally {
      setRetrying(false);
    }
  };

  return (
    <div className="min-h-screen bg-canvas px-6 py-12 text-fg">
      <div className="pointer-events-none fixed inset-0 opacity-40" style={{ backgroundImage: 'radial-gradient(var(--app-texture-dot) 1px, transparent 1px)', backgroundSize: '24px 24px' }} />
      <main className="relative mx-auto max-w-4xl">
        <div className={`overflow-hidden rounded-3xl border bg-surface shadow-[0_30px_90px_rgba(15,23,42,0.12)] ${failed ? 'border-warn-line' : 'border-info-line'}`}>
          <div className={`border-b bg-gradient-to-r via-surface to-surface px-8 py-7 ${failed ? 'border-warn-line from-warn-soft' : 'border-info-line from-info-soft'}`}>
            <div className="flex items-start gap-4">
              <div className={`flex h-12 w-12 shrink-0 items-center justify-center rounded-2xl ${failed ? 'bg-warn-soft text-warn' : 'bg-info-soft text-info'}`}>
                {failed ? <AlertTriangle size={24} /> : <LoaderCircle size={24} className="animate-spin" />}
              </div>
              <div>
                <div className={`text-xs font-semibold uppercase tracking-[0.16em] ${failed ? 'text-warn' : 'text-info'}`}>{failed ? '安全恢复模式' : '安全迁移模式'}</div>
                <h1 className="mt-1 text-2xl font-bold">{failed ? 'Claude 端点迁移未完成' : '正在迁移 Claude 端点'}</h1>
                <p className="mt-2 max-w-2xl text-sm leading-6 text-fg-body">
                  {failed
                    ? '代理、后台检测和历史写入尚未启动，避免在数据库结构不一致时继续处理请求。请先查看备份与失败阶段，再执行幂等重试。'
                    : '正在创建一致性备份并升级数据库结构。迁移完成前代理、后台检测和历史写入不会启动。'}
                </p>
              </div>
            </div>
          </div>

          <div className="space-y-6 px-8 py-7">
            <div className={`rounded-2xl border px-4 py-3 text-sm leading-6 ${failed ? 'tone-rose' : 'tone-sky'}`}>
              <div className="font-semibold">{failed ? '失败阶段' : '当前阶段'}：{status.phase || '准备阶段'}</div>
              <div className="mt-1 break-words">{failed ? (retryError || status.error || '未提供错误信息') : '状态会自动刷新，请保持应用运行。'}</div>
            </div>

            <div className="grid gap-3 md:grid-cols-2">
              <Detail icon={Database} label="运行数据库" value={status.databasePath} />
              <Detail icon={FileCog} label="配置文件" value={status.configPath} />
              <Detail icon={Archive} label="迁移备份" value={status.backupDir} />
              <Detail icon={ShieldCheck} label="完整性" value={`数据库 ${status.databaseIntegrity || '待验证'} · 备份 ${status.backupIntegrity || '待验证'}`} />
            </div>

            {(status.endpointCountBefore > 0 || status.endpointCountAfter > 0) && (
              <div className="rounded-2xl border border-line bg-surface-sub px-4 py-3 text-sm text-fg-body">
                端点记录 {status.endpointCountBefore || 0} → {status.endpointCountAfter || 0}
                {status.derivedRecordCount > 0 ? ` · 新派生 ${status.derivedRecordCount}` : ''}
              </div>
            )}

            <div className="flex flex-col gap-3 rounded-2xl border border-line bg-surface-sub px-4 py-4 sm:flex-row sm:items-center sm:justify-between">
              <div>
                <div className="text-sm font-semibold text-fg">{failed ? '重试会从已提交阶段继续' : '迁移完成后会自动进入主界面'}</div>
                <div className="mt-1 text-xs leading-5 text-fg-muted">{failed ? '不会删除现有备份，也不会启动代理，直到 schema、配置与完整性验证全部完成。' : '数据库迁移与配置切换完成前，不会开放业务写入。'}</div>
              </div>
              {failed ? (
                <button type="button" onClick={retry} disabled={retrying || status.retryAllowed === false} className="inline-flex shrink-0 items-center justify-center gap-2 rounded-xl bg-inverted px-4 py-2.5 text-sm font-semibold text-fg-inverted shadow-lg transition hover:bg-inverted/85 disabled:cursor-not-allowed disabled:opacity-50">
                  <RefreshCw size={16} className={retrying ? 'animate-spin' : ''} />{retrying ? '正在重试' : '重试迁移'}
                </button>
              ) : (
                <div className="inline-flex shrink-0 items-center gap-2 text-sm font-semibold text-info"><LoaderCircle size={16} className="animate-spin" />迁移进行中</div>
              )}
            </div>
          </div>
        </div>
      </main>
    </div>
  );
};

export default MigrationRecoveryPage;
