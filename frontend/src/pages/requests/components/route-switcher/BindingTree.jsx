// 模型路由列表：一行一个映射 / 绑定，右侧直接写当前策略；点开才列出它的目标（同一时间只展开一个）
// 2026-10-07 桌面化：替代原来的左右两栏（BindingList + 右侧目标列表）
import { ChevronRight, Globe } from 'lucide-react';
import useChangeFlash from './useChangeFlash.js';

const BindingTree = ({ bindings = [], expandedId = null, onToggle, isWebTools, summaryOf, strategyKeyOf, renderTargets }) => {
  const flashes = useChangeFlash(strategyKeyOf ? bindings.map((binding) => [binding.assignment_id, strategyKeyOf(binding)]) : []);
  return (
    <div>
      {bindings.map((binding) => {
        const expanded = binding.assignment_id === expandedId;
        const webTools = Boolean(isWebTools?.(binding));
        const name = webTools ? '联网工具' : binding.model;
        return (
          <div key={binding.assignment_id}>
            <button
              type="button"
              aria-expanded={expanded}
              onClick={() => onToggle?.(expanded ? null : binding.assignment_id)}
              className="flex h-[26px] w-full items-center gap-1.5 rounded-[5px] pl-1.5 pr-2 text-left hover:bg-surface-mut"
            >
              <span className="flex w-3.5 shrink-0 justify-center text-fg-subtle">
                <ChevronRight size={12} className={`transition-transform ${expanded ? 'rotate-90' : ''}`} />
              </span>
              {webTools && <Globe size={13} className="shrink-0 text-fg-muted" />}
              <span
                className={`min-w-0 flex-1 truncate ${webTools ? '' : 'font-mono text-[12.5px]'} ${binding.enabled ? 'text-fg' : 'text-fg-subtle line-through'}`}
                title={webTools ? name : binding.model}
              >
                {name}
              </span>
              <span className={`max-w-[55%] shrink-0 truncate text-[11.5px] transition-colors duration-700 ${flashes.has(binding.assignment_id) ? 'text-accent' : 'text-fg-subtle'}`}>{summaryOf?.(binding)}</span>
            </button>
            {expanded && renderTargets?.(binding)}
          </div>
        );
      })}
    </div>
  );
};

export default BindingTree;
