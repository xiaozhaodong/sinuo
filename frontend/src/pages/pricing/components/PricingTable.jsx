// ============================================
// 基础定价表格：一行一个模型，默认定价置顶并以星标标出
// 2026-10-07：替代原来的卡片网格
// ============================================

import { Star } from 'lucide-react';
import { formatPriceCell } from '../utils/pricingForm.js';

const PRICE_COLUMNS = [
  { key: 'inputPrice', label: '输入', tone: 'text-fg' },
  { key: 'outputPrice', label: '输出', tone: 'text-fg' },
  { key: 'cacheCreationPrice5m', label: '缓存写 5m', tone: 'text-fg-muted', zeroAsDash: true },
  { key: 'cacheCreationPrice1h', label: '缓存写 1h', tone: 'text-fg-muted', zeroAsDash: true },
  { key: 'cacheReadPrice', label: '缓存读取', tone: 'text-fg-muted', zeroAsDash: true }
];

const PricingTable = ({ pricings, selectedId, onSelect, onRowContextMenu }) => (
  <table className="desk-table">
    <thead>
      <tr>
        <th className="w-7" aria-label="默认定价" />
        <th>模型 ID</th>
        {PRICE_COLUMNS.map((column) => (
          <th key={column.key} className="text-right">{column.label}</th>
        ))}
      </tr>
    </thead>
    <tbody>
      {pricings.map((pricing) => (
        <tr
          key={pricing.modelName}
          data-row-id={pricing.modelName}
          aria-selected={selectedId === pricing.modelName}
          title={pricing.displayName || undefined}
          onClick={() => onSelect(pricing.modelName)}
          onContextMenu={(event) => onRowContextMenu(event, pricing)}
        >
          <td className="text-warn-solid">
            {pricing.isDefault && <Star size={14} fill="currentColor" aria-label="默认定价" />}
          </td>
          <td className="font-mono text-[12.5px] text-fg">{pricing.modelName}</td>
          {PRICE_COLUMNS.map((column) => (
            <td key={column.key} className={`text-right tabular-nums ${column.tone}`}>
              {formatPriceCell(pricing[column.key], { zeroAsDash: column.zeroAsDash })}
            </td>
          ))}
        </tr>
      ))}
    </tbody>
  </table>
);

export default PricingTable;
