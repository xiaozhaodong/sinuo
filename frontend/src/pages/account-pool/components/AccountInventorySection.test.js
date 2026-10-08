import test from 'node:test';
import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const __dirname = path.dirname(fileURLToPath(import.meta.url));

const loadSource = async (relativePath) => {
  const sourcePath = path.resolve(__dirname, relativePath);
  return readFile(sourcePath, 'utf8');
};

test('inventory workspace exposes saved views, batch actions, and drawer sections', async () => {
  const [sectionSource, filtersSource, tableSource, drawerSource, hookSource] = await Promise.all([
    loadSource('./AccountInventorySection.jsx'),
    loadSource('./AccountInventoryFilters.jsx'),
    loadSource('./AccountInventoryTable.jsx'),
    loadSource('./AccountDetailsDrawer.jsx'),
    loadSource('../hooks/useAccountPoolDashboardState.js')
  ]);

  assert.match(hookSource, /主组异常/, 'expected first-version saved view 主组异常 to be seeded by the inventory state helper');
  assert.match(hookSource, /待处理 OAuth/, 'expected first-version saved view 待处理 OAuth to be seeded by the inventory state helper');
  assert.match(hookSource, /额度风险/, 'expected first-version saved view 额度风险 to be seeded by the inventory state helper');
  assert.match(hookSource, /冷备 free 账号/, 'expected first-version saved view 冷备 free 账号 to be seeded by the inventory state helper');
  assert.match(filtersSource, /搜索账号|搜索/, 'expected the toolbar to expose search affordance copy');

  assert.match(filtersSource, /批量测试/, 'expected the batch bar to expose 批量测试');
  assert.match(filtersSource, /批量刷新画像/, 'expected the batch bar to expose 批量刷新画像');
  assert.match(filtersSource, /批量启用|批量停用|启用-停用/, 'expected the batch bar to expose enable\/disable affordance');
  assert.match(filtersSource, /移到主组|移到备组|移到冷备/, 'expected the batch bar to expose tier move affordances');
  assert.match(filtersSource, /个已选|batchFeedback\.message/, 'expected the batch bar copy to mention affected count');
  assert.match(tableSource, /checkbox|aria-label="选择账号"/, 'expected rows to support explicit selection affordance');
  assert.match(tableSource, /启用账号|停用账号/, 'expected row-level quick toggle affordance to remain visible');
  assert.match(tableSource, /更多|查看详情/, 'expected row-level secondary action affordance to remain visible');
  // 2026-10-07 桌面化：分页收进状态栏，控件由共享的 StatusPager 提供
  const pagerSource = await loadSource('../../../components/desktop/StatusPager.jsx');
  assert.match(sectionSource, /<StatusPager[\s\S]*onPageChange=\{state\.pagination\.goToPage\}[\s\S]*onPageSizeChange=\{state\.pagination\.setPageSize\}/, 'expected the inventory workspace to wire pagination into the status bar pager');
  assert.match(pagerSource, /上一页/, 'expected the pager to expose 上一页');
  assert.match(pagerSource, /下一页/, 'expected the pager to expose 下一页');
  assert.match(pagerSource, /每页/, 'expected the pager to expose page-size controls');
  assert.match(drawerSource, /编辑账号/, 'expected the drawer action area to expose 编辑账号');

  assert.match(drawerSource, /账号详情/, 'expected the drawer to expose account detail heading');
  assert.match(drawerSource, /额度/, 'expected the drawer to expose 额度 section');
  assert.match(drawerSource, /健康/, 'expected the drawer to expose 健康 section');
  assert.match(drawerSource, /路由配置/, 'expected the drawer to expose 路由配置 section');
  assert.match(drawerSource, /动作/, 'expected the drawer to expose 动作 section');
});

test('dashboard state helper provides reusable saved views and batch feedback copy', async () => {
  const hookModule = await import('../hooks/useAccountPoolDashboardState.js');

  assert.deepEqual(
    hookModule.DEFAULT_INVENTORY_SAVED_VIEWS.map((item) => item.label),
    ['主组异常', '待处理 OAuth', '额度风险', '冷备 free 账号'],
    'expected the inventory state hook to seed the reviewed saved views'
  );

  assert.ok(
    hookModule.INVENTORY_BATCH_ACTIONS.some((item) => item.label === '批量测试'),
    'expected the inventory state hook to expose 批量测试 as a reusable action'
  );
  assert.ok(
    hookModule.INVENTORY_BATCH_ACTIONS.some((item) => item.label === '批量刷新画像'),
    'expected the inventory state hook to expose 批量刷新画像 as a reusable action'
  );
  assert.ok(
    hookModule.INVENTORY_BATCH_ACTIONS.some((item) => item.label.includes('批量启用-停用')),
    'expected the inventory state hook to expose the enable/disable batch affordance'
  );
  assert.ok(
    hookModule.INVENTORY_BATCH_ACTIONS.some((item) => item.label.includes('批量移到主组-备组-冷备')),
    'expected the inventory state hook to expose the reviewed tier batch affordance'
  );
  assert.deepEqual(
    hookModule.DEFAULT_INVENTORY_PAGE_SIZES,
    [20, 50, 100],
    'expected the inventory state hook to expose reviewed page-size options'
  );
  assert.equal(
    hookModule.INVENTORY_BATCH_ACTIONS.find((item) => item.key === 'batch-move-tier')?.variants?.find((item) => item.key === 'primary')?.label,
    '移到主组',
    'expected 批量移到主组 to be exposed as a first-class tier action'
  );
  assert.notEqual(
    hookModule.INVENTORY_BATCH_ACTIONS.find((item) => item.key === 'batch-move-tier')?.variants?.find((item) => item.key === 'cold')?.disabled,
    true,
    'expected 批量移到冷备 to be executable instead of disabled'
  );
  assert.deepEqual(
    hookModule.paginateInventoryRows(Array.from({ length: 55 }, (_, index) => ({ id: index + 1 })), { currentPage: 4, pageSize: 20 }),
    {
      rows: Array.from({ length: 15 }, (_, index) => ({ id: index + 41 })),
      currentPage: 3,
      pageSize: 20,
      totalPages: 3,
      totalCount: 55
    },
    'expected pagination helper to clamp page number and slice the current page rows'
  );

  assert.equal(
    hookModule.buildBatchActionNotice('批量测试', 3, { phase: 'intent' }),
    '准备对 3 个账号执行批量测试',
    'expected batch intent copy to mention the affected count'
  );
  assert.equal(
    hookModule.buildBatchActionNotice('批量刷新画像', 2, { phase: 'success' }),
    '已对 2 个账号完成批量刷新画像',
    'expected batch success copy to mention the affected count'
  );
});

test('inventory workspace threads busy state into the reviewed table and drawer actions', async () => {
  const [sectionSource, tableSource, drawerSource] = await Promise.all([
    loadSource('./AccountInventorySection.jsx'),
    loadSource('./AccountInventoryTable.jsx'),
    loadSource('./AccountDetailsDrawer.jsx')
  ]);

  assert.match(sectionSource, /busyKey\s*=\s*''|busyKey\b/, 'expected inventory section props to accept busyKey');
  assert.match(sectionSource, /AccountInventoryTable[\s\S]*busyKey=\{busyKey\}/, 'expected inventory section to forward busyKey into the table');
  assert.match(sectionSource, /AccountDetailsDrawer[\s\S]*busyKey=\{busyKey\}/, 'expected inline drawer rendering to forward busyKey');
  assert.match(tableSource, /disabled=\{[^}]*busy/i, 'expected row-level quick actions to disable while busy');
  assert.match(drawerSource, /disabled=\{[^}]*busy/i, 'expected drawer actions to disable while busy');
});

test('inventory grid cards keep quick actions visible when account timestamps are long', async () => {
  const cardSource = await loadSource('./AccountInventoryCard.jsx');

  assert.match(
    cardSource,
    /<QuotaBar label="d7"[\s\S]*?\{statusText\}[\s\S]*?className="mt-auto flex items-center justify-between/,
    'expected the card status text below quota and before the quick-action footer'
  );
  assert.match(
    cardSource,
    /className="mt-2 min-w-0 truncate whitespace-nowrap text-\[11px\] text-fg-muted"/,
    'expected long connectivity and refresh timestamps to use a full-width muted row with truncation'
  );
  assert.match(
    cardSource,
    /title=\{statusText\}/,
    'expected truncated connectivity and refresh timestamps to remain available on hover'
  );
  assert.match(
    cardSource,
    /className="flex shrink-0 items-center gap-0\.5"/,
    'expected the card quick-action group to remain fixed and fully visible'
  );
});
