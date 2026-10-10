import { STATUS_TAG_CLASSES } from './codexCatalogView.js';

const CodexModelsTable = ({ models, summaries, selectedId, saving, onSelect, onToggleEnabled, onRowContextMenu }) => (
  <table className="desk-table">
    <thead>
      <tr>
        <th className="w-12 text-center">启用</th>
        <th>模型 ID</th>
        <th>显示名称</th>
        <th>来源</th>
        <th className="text-right">上下文</th>
        <th>思考等级</th>
        <th>状态</th>
      </tr>
    </thead>
    <tbody>
      {models.map((model) => {
        const summary = summaries.get(model.id);
        const displayName = model.display_name || model.id;
        return (
          <tr
            key={model.id}
            data-row-id={model.id}
            aria-selected={selectedId === model.id}
            onClick={() => onSelect(model.id)}
            onContextMenu={(event) => onRowContextMenu(event, model)}
            className={!model.enabled ? 'text-fg-subtle' : undefined}
          >
            <td className="text-center">
              <input
                type="checkbox"
                className="app-checkbox mx-auto"
                checked={model.enabled}
                disabled={saving}
                aria-label={`启用模型 ${model.id}`}
                onClick={(event) => event.stopPropagation()}
                onChange={(event) => onToggleEnabled(model.id, event.target.checked)}
              />
            </td>
            <td className={`font-mono text-[12.5px] ${model.enabled ? 'text-fg' : 'text-fg-subtle'}`}>
              <span className="block max-w-[220px] truncate" title={model.id}>{model.id}</span>
            </td>
            <td><span className="block max-w-[160px] truncate" title={displayName}>{displayName}</span></td>
            <td className="text-xs text-fg-muted">{model.source === 'official' ? '官方' : '自定义'}</td>
            <td className="text-right text-xs tabular-nums">{summary.contextWindow}</td>
            <td className="text-xs text-fg-muted" title={summary.reasoningTitle}>{summary.reasoningRange}</td>
            <td>
              <div className="flex gap-1 whitespace-nowrap">
                {summary.tags.map((tag) => (
                  <span key={tag.key} title={tag.title} className={`rounded border px-1.5 text-[11px] leading-[18px] ${STATUS_TAG_CLASSES[tag.tone]}`}>{tag.label}</span>
                ))}
              </div>
            </td>
          </tr>
        );
      })}
    </tbody>
  </table>
);

export default CodexModelsTable;
