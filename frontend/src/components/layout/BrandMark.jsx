// ============================================
// 布局组件 - 品牌标识
// 应用图标（build/appicon.png）中 S 的扁平单色版，沿中心线描摹、圆头描边；
// 颜色跟随 currentColor，页头与启动画面共用
// ============================================

const BrandMark = ({ className }) => (
  <svg
    viewBox="0 0 24 24"
    fill="none"
    stroke="currentColor"
    strokeWidth={5}
    strokeLinecap="round"
    strokeLinejoin="round"
    aria-hidden="true"
    className={className}
  >
    <path d="M19.12 2.5L11.61 2.5C7.79 2.5 6.23 4.67 6.23 7.01C6.23 9.96 8.66 11.35 12.91 12.22C17.34 13.13 18.21 15.17 18.21 17.42C18.21 20.03 16.12 21.5 12.74 21.5L4.88 21.5" />
  </svg>
);

export default BrandMark;
