import { useAccountPoolAccounts, useLatestScheduleSnapshot, useNotice } from '@pages/account-pool/hooks';

const useCodexPoolData = ({ enabled }) => {
  const { showNotice } = useNotice({ enabled });
  const { accounts, loading, error, loadData } = useAccountPoolAccounts();
  const { latestScheduleSnapshot, snapshotUnsupported, loadLatestScheduleSnapshot } = useLatestScheduleSnapshot({ showNotice, enabled });
  return { accounts, loading, error, loadData, latestScheduleSnapshot, snapshotUnsupported, loadLatestScheduleSnapshot };
};

export default useCodexPoolData;
