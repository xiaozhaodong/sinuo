import { useState } from 'react';
import { AlertTriangle, Archive, Database, FileCog, LoaderCircle, RefreshCw, ShieldCheck } from 'lucide-react';
import { Button } from '@components/ui';
import { retryStartupMigration } from '@utils/wailsApi.js';

const Detail = ({ icon: Icon, label, value }) => (
  <div className="py-2">
    <div className="flex items-center gap-2 text-xs text-fg-muted"><Icon size={14} />{label}</div>
    <div className="selectable mt-1 break-all font-mono text-xs leading-5 text-fg-body">{value || '未生成'}</div>
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
    <div className="flex min-h-screen items-center justify-center bg-pane px-4 py-8 text-fg">
      <main className="w-full max-w-[480px] rounded-xl border border-line bg-surface p-5">
        {failed
          ? <AlertTriangle size={28} strokeWidth={1.75} className="text-warn" />
          : <LoaderCircle size={28} strokeWidth={1.75} className="animate-spin text-info" />}
        <div className={`mt-3 text-xs ${failed ? 'text-warn' : 'text-info'}`}>{failed ? '安全恢复模式' : '安全迁移模式'}</div>
        <h1 className="mt-1 text-[13px] font-semibold">{failed ? 'Claude 端点迁移未完成' : '正在迁移 Claude 端点'}</h1>
        <p className="mt-1.5 text-xs leading-relaxed text-fg-body">
          {failed
            ? '代理、后台检测和历史写入尚未启动，避免在数据库结构不一致时继续处理请求。请先查看备份与失败阶段，再执行幂等重试。'
            : '正在创建一致性备份并升级数据库结构。迁移完成前代理、后台检测和历史写入不会启动。'}
        </p>

        <div className={`mt-4 flex items-start gap-2 rounded-lg border px-3 py-2 text-xs ${failed ? 'border-danger-line bg-danger-soft text-danger' : 'border-info-line bg-info-soft text-info'}`}>
          <div className="min-w-0">
            <div className="font-semibold">{failed ? '失败阶段' : '当前阶段'}：{status.phase || '准备阶段'}</div>
            <div className="selectable mt-1 break-words">{failed ? (retryError || status.error || '未提供错误信息') : '状态会自动刷新，请保持应用运行。'}</div>
          </div>
        </div>

        <div className="mt-3 divide-y divide-line-soft">
          <Detail icon={Database} label="运行数据库" value={status.databasePath} />
          <Detail icon={FileCog} label="配置文件" value={status.configPath} />
          <Detail icon={Archive} label="迁移备份" value={status.backupDir} />
          <Detail icon={ShieldCheck} label="完整性" value={`数据库 ${status.databaseIntegrity || '待验证'} · 备份 ${status.backupIntegrity || '待验证'}`} />
        </div>
        {(status.endpointCountBefore > 0 || status.endpointCountAfter > 0) && (
          <div className="mt-2 border-t border-line-soft py-2 text-xs text-fg-body">
            端点记录 {status.endpointCountBefore || 0} → {status.endpointCountAfter || 0}
            {status.derivedRecordCount > 0 ? ` · 新派生 ${status.derivedRecordCount}` : ''}
          </div>
        )}

        <div className="mt-4 border-t border-line-soft pt-3">
          <div className="text-[13px] font-medium text-fg">{failed ? '重试会从已提交阶段继续' : '迁移完成后会自动进入主界面'}</div>
          <div className="mt-1 text-xs leading-5 text-fg-muted">{failed ? '不会删除现有备份，也不会启动代理，直到 schema、配置与完整性验证全部完成。' : '数据库迁移与配置切换完成前，不会开放业务写入。'}</div>
          <div className="mt-4 flex justify-end">
            {failed ? (
              <Button type="button" onClick={retry} disabled={retrying || status.retryAllowed === false}>
                <RefreshCw size={14} className={`mr-1.5 ${retrying ? 'animate-spin' : ''}`} />
                {retrying ? '正在重试' : '重试迁移'}
              </Button>
            ) : (
              <div className="inline-flex items-center gap-2 text-xs text-info"><LoaderCircle size={14} className="animate-spin" />迁移进行中</div>
            )}
          </div>
        </div>
      </main>
    </div>
  );
};

export default MigrationRecoveryPage;
