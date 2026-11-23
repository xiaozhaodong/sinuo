// ============================================
// 挂到 body 的浮层（下拉菜单等）标记
// ============================================
// 浮层走 Portal 后不再是触发它的弹窗 / 浮层面板的 DOM 后代，
// 焦点陷阱与「点外部关闭」需要靠这个标记把它认作内部区域。

export const FLOATING_LAYER_ATTR = 'data-floating-layer';

export const isInFloatingLayer = (target) => Boolean(target?.closest?.(`[${FLOATING_LAYER_ATTR}]`));
