import { useId } from 'react';
import { RefreshCw } from 'lucide-react';
import { Button } from '@components/ui';
import { AlertDialog } from '@components/ui/ConfirmDialog.jsx';

const AppUpdateRestartDialog = ({ activeRequests, onChoose }) => {
  const messageId = useId();
  const cancel = () => onChoose('');

  return (
    <AlertDialog title="重启以完成更新" describedBy={messageId} width={400} onClose={cancel}>
      <RefreshCw size={28} strokeWidth={1.75} className="text-accent-fg" />
      <h3 className="mt-3 text-[13px] font-semibold text-fg">重启以完成更新</h3>
      <p id={messageId} className="selectable mt-1.5 text-xs leading-relaxed text-fg-body">仍有 {activeRequests} 个请求进行中。立即重启会中断这些请求。</p>
      <div className="mt-5 flex flex-wrap justify-end gap-2">
        <Button type="button" variant="secondary" onClick={cancel}>取消</Button>
        <Button type="button" variant="secondary" onClick={() => onChoose('now')}>立即重启</Button>
        <Button type="button" onClick={() => onChoose('wait')}>等待完成后重启</Button>
      </div>
    </AlertDialog>
  );
};

export default AppUpdateRestartDialog;
