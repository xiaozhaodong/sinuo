import { buildQueryParamsFromFilters } from './filterTime.js';

const buildTimeRangeSelectionState = (currentFilters = {}, nextTimeRange = {}, filterPatch = {}) => {
  const filters = {
    ...currentFilters,
    ...filterPatch,
    ...nextTimeRange
  };

  return {
    filters,
    appliedQueryParams: buildQueryParamsFromFilters(filters)
  };
};

export { buildTimeRangeSelectionState };
