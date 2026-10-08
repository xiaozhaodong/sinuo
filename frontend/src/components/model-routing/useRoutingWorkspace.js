import { useMemo, useState } from 'react';
import useContextMenu from '@components/desktop/useContextMenu.jsx';
import { filterRoutingBindings, routingCounts } from './routingView.js';

export const useRoutingWorkspace = ({ data, actions, confirm, config }) => {
  const [query, setQuery] = useState('');
  const [filter, setFilter] = useState('all');
  const [selectedId, setSelectedId] = useState(null);
  const { contextMenu, openContextMenu } = useContextMenu();
  const displayName = config.bindingDisplayName;
  const rows = useMemo(() => filterRoutingBindings(data.bindings, { query, filter, displayName }), [data.bindings, query, filter, displayName]);
  const selected = rows.find((binding) => binding.assignment_id === selectedId) || null;
  const disabled = actions.busy || data.loading || data.readFailed || !data.settings || !data.ready;
  const onToggle = async (enabled) => {
    if (enabled && routingCounts(data.bindings).enabled === 0 && !(await confirm(config.enableConfirmation(data.bindings)))) return;
    return actions.run(() => config.api.updateSettings({ [config.settingsField]: enabled }));
  };
  const onDelete = async (binding) => {
    if (!(await confirm({ ...config.deleteConfirmation(binding), confirmText: '确认删除', danger: true }))) return;
    return actions.run(() => config.api.deleteBinding(binding.assignment_id));
  };
  const onRowContextMenu = (event, binding) => {
    setSelectedId(binding.assignment_id);
    openContextMenu(event, [
      { label: '编辑…', disabled, onSelect: () => actions.setEditor(binding) },
      { label: binding.enabled ? '停用' : '启用', disabled, onSelect: () => actions.patch(binding, { enabled: !binding.enabled }) },
      { label: '恢复自动', disabled: disabled || binding.route_mode === 'auto', onSelect: () => actions.patch(binding, config.restoreAutoPatch(binding)) },
      '-', { label: '删除…', danger: true, disabled, onSelect: () => onDelete(binding) }
    ]);
  };
  return { query, setQuery, filter, setFilter, rows, selected, disabled, setSelectedId, onToggle, onDelete, onRowContextMenu, contextMenu };
};
