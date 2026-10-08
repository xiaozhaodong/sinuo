// Account Pool 共享表单组件
import { useRef } from 'react';
import { Button } from '@components/ui';
import Sheet from '@components/desktop/Sheet.jsx';

const FormField = ({ label, required = false, children }) => (
  <label className="flex items-start gap-3 px-3.5 py-2">
    <span className="w-[112px] shrink-0 pt-[3px] text-right text-[12.5px] text-fg-body">
      {label}{required && <span className="ml-1 text-danger">*</span>}
    </span>
    <span className="block min-w-0 flex-1">{children}</span>
  </label>
);

const Modal = ({
  open, title, submitText, submitVariant = 'primary', cancelText = '取消',
  onClose, onSubmit, submitting, children
}) => {
  const cancelRef = useRef(null);
  const handleRequestClose = () => { if (!submitting) onClose(); };
  return (
    <Sheet open={open} title={title} label={title} width={560} onClose={handleRequestClose}
      onSubmit={onSubmit} closeDisabled={submitting} initialFocusRef={cancelRef}
      footer={<>
        <Button ref={cancelRef} type="button" variant="secondary" onClick={handleRequestClose} disabled={submitting}>{cancelText}</Button>
        <Button type="submit" variant={submitVariant} loading={submitting}>{submitText}</Button>
      </>}>
      <div className="space-y-4 pt-4 text-[13px]">{children}</div>
    </Sheet>
  );
};

export { FormField, Modal };
