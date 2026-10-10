import test from 'node:test';
import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const sourcePath = path.resolve(__dirname, './index.jsx');

test('AccountPoolPage wires shared pool data, inventory workspace, and header actions together', async () => {
  const source = await readFile(sourcePath, 'utf8');

  assert.match(source, /AccountInventorySection/);
  assert.doesNotMatch(source, /SchedulerDrawer/);
  assert.match(source, /buildAccountPoolDashboardModel/);
  assert.match(source, /openCreateAccount/);
  assert.match(source, /openEditAccount/);
  assert.match(source, /PageHeader[\s\S]*onCreate=/);
  assert.match(source, /PageHeader[\s\S]*onRefresh=/);
  assert.match(source, /poolData, inventoryViewRequest/);
  assert.match(source, /AccountDetailsDrawer[\s\S]*onEditAccount=/);
  assert.match(source, /accounts, loading, error, loadData, latestScheduleSnapshot, loadLatestScheduleSnapshot \} = poolData/);
  assert.match(source, /AccountInventorySection[\s\S]*inventory=\{dashboardModel\.inventory\}[\s\S]*busyKey=\{busyKey\}/);
  assert.match(source, /AccountInventorySection[\s\S]*externalViewRequest=/);
  assert.match(source, /externalViewRequest=\{inventoryViewRequest\}/);
  assert.match(await readFile(path.resolve(__dirname, '../codex/schedule/useSchedulerActions.js'), 'utf8'), /setGroupActiveAccount\(groupKey, accountId\)/);
  assert.doesNotMatch(source, /handleMoveAccountToTier\(account,\s*'primary'\)/);
  assert.doesNotMatch(source, /handleMoveAccountToTier\(account,\s*'backup'\)/);
  assert.doesNotMatch(source, /label: '概览'/);
  assert.doesNotMatch(source, /label: '异常中心'/);
  assert.doesNotMatch(source, /AccountPoolOverviewSection/);
  assert.doesNotMatch(source, /AccountPoolAnomalySection/);
});
