import { useRef, useState } from 'react';
import { resolveErrorMessage } from '@utils/errors.js';

export const useBindingActions = ({ data, createBinding, updateBinding, buildPatch }) => {
  const [busy, setBusy] = useState(false);
  const [editor, setEditor] = useState(null);
  const savingRef = useRef(false);
  const run = async (operation) => {
    if (savingRef.current) return false;
    savingRef.current = true;
    setBusy(true);
    data.setError('');
    try {
      await operation();
      // 保存已经成功；重新载入失败时仍在页签中显示错误并禁用编辑。
      await data.load();
      return true;
    } catch (failure) {
      data.setError(resolveErrorMessage(failure, '保存模型路由失败'));
      return false;
    } finally {
      savingRef.current = false;
      setBusy(false);
    }
  };
  const save = async (id, payload) => {
    if (await run(() => id ? updateBinding(id, payload) : createBinding(payload))) setEditor(null);
  };
  const patch = (binding, change) => run(() => updateBinding(binding.assignment_id, buildPatch(binding, change)));
  return { busy, editor, setEditor, run, save, patch };
};
