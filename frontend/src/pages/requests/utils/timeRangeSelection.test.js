import test from 'node:test';
import assert from 'node:assert/strict';

import { buildTimeRangeSelectionState } from './timeRangeSelection.js';

test('只看此会话时将近 30 天与会话条件合并为同一次查询状态', () => {
  const current = { sessionId: 'old', model: 'gpt-test', upstreamName: 'account-a', startDate: '2026-10-09T00:00', endDate: '2026-10-10T00:00' };
  const range = { startDate: '2026-09-10T00:00', endDate: '2026-10-10T00:00' };
  const result = buildTimeRangeSelectionState(current, range, { sessionId: 'new-session' });
  assert.deepEqual(result.filters, { ...current, ...range, sessionId: 'new-session' });
  assert.deepEqual(result.appliedQueryParams, {
    start_date: '2026-09-10T00:00:00', end_date: '2026-10-10T00:00:00',
    model: 'gpt-test', upstream_name: 'account-a', session_id: 'new-session'
  });
  assert.equal(current.sessionId, 'old', '构造查询不得改写原筛选快照');
  assert.equal(buildTimeRangeSelectionState(result.filters, range).appliedQueryParams.session_id, 'new-session');
});

test('buildTimeRangeSelectionState updates filters and applied query params together', () => {
  const currentFilters = {
    startDate: '2026-03-14T00:00',
    endDate: '2026-03-14T23:59',
    status: 'success',
    model: 'gpt-4.1',
    requestFamily: 'codex',
    upstreamName: 'all'
  };
  const nextTimeRange = {
    startDate: '2026-03-08T00:00',
    endDate: '2026-03-15T00:00'
  };

  const result = buildTimeRangeSelectionState(currentFilters, nextTimeRange);

  assert.deepEqual(result.filters, {
    ...currentFilters,
    ...nextTimeRange
  });
  assert.deepEqual(result.appliedQueryParams, {
    start_date: '2026-03-08T00:00:00',
    end_date: '2026-03-15T00:00:00',
    status: 'success',
    model: 'gpt-4.1',
    request_family: 'codex'
  });
});
