const ClaudeModelsTable = ({ models, selectedId, saving, onSelect, onToggleEnabled, onRowContextMenu }) => (
  <table className="desk-table">
    <thead>
      <tr>
        <th className="w-12 text-center">启用</th>
        <th>模型 ID</th>
        <th>显示名称</th>
      </tr>
    </thead>
    <tbody>
      {models.map((model) => {
        const displayName = model.display_name || model.id;
        const muted = !model.enabled;
        return (
          <tr
            key={model.id}
            data-row-id={model.id}
            aria-selected={selectedId === model.id}
            onClick={() => onSelect(model.id)}
            onContextMenu={(event) => onRowContextMenu(event, model)}
            className={muted ? 'text-fg-subtle' : undefined}
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
              <span className="block max-w-[320px] truncate" title={model.id}>{model.id}</span>
            </td>
            <td>
              <span className={`block truncate ${displayName === model.id ? 'text-fg-subtle' : ''}`} title={displayName}>
                {displayName === model.id ? '—' : displayName}
              </span>
            </td>
          </tr>
        );
      })}
    </tbody>
  </table>
);

export default ClaudeModelsTable;
