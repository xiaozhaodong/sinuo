import { buildQueryParamsFromFilters } from './filterTime.js';

const buildTimeRangeSelectionState = (currentFilters = {}, nextTimeRange = {}) => {
  const filters = {
    ...currentFilters,
    ...nextTimeRange
  };

  return {
    filters,
    appliedQueryParams: buildQueryParamsFromFilters(filters)
  };
};

export { buildTimeRangeSelectionState };
