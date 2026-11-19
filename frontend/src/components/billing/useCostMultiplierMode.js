import { useState } from 'react';
import { isSplitMultiplier, isUnifiedMultiplierInvalid } from './billingForms.js';

const modeOf = (total) => (isSplitMultiplier(total) ? 'split' : 'unified');

// 成本倍率的「统一 / 分项」模式。总倍率为 0 即分项，所以切到分项要把总倍率写成 0，
// 切回统一时恢复切走前的统一值，没有就用 1。
// select 返回新的总倍率，由调用方写回自己的表单状态；reset 用于表单重新打开时按总倍率重算模式。
export default function useCostMultiplierMode(initialTotal) {
  const [state, setState] = useState(() => ({ mode: modeOf(initialTotal), saved: null }));

  const select = (next, currentTotal) => {
    if (next === state.mode) return currentTotal;
    if (next === 'split') {
      setState({ mode: 'split', saved: isUnifiedMultiplierInvalid(currentTotal) ? null : currentTotal });
      return 0;
    }
    setState({ mode: 'unified', saved: null });
    return state.saved ?? 1;
  };
  const reset = (total) => setState({ mode: modeOf(total), saved: null });

  return { mode: state.mode, select, reset };
}
