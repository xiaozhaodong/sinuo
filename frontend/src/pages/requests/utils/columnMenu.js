// 工具栏与表头右键共用列菜单；列定义、可见状态和修改回调仍由 useColumnConfig 提供。
export const buildColumnMenuItems = (columns, visibleColumns, onToggleColumn, onResetColumns) => [
  { header: '显示列' },
  ...columns
    .filter((column) => column.alwaysVisible !== true)
    .map((column) => ({
      label: column.label,
      checked: visibleColumns.includes(column.id),
      onSelect: () => onToggleColumn(column.id)
    })),
  '-',
  { label: '恢复默认列', onSelect: onResetColumns }
];
