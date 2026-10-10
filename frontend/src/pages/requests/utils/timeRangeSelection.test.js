import test from 'node:test';
import assert from 'node:assert/strict';

import { buildTimeRangeSelectionState } from './timeRangeSelection.js';

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
