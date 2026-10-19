// 空 mode 在空闲时直接重启：必须在调用前完成确认，不能事后再询问。
export const runAppUpdateRestart = async ({ readStatus, confirmIdle, chooseRequests, restart }) => {
  const status = await readStatus();
  if (status.state !== 'ready') throw new Error('更新尚未就绪，请稍后重试');
  if (status.active_requests > 0) {
    const mode = await chooseRequests(status.active_requests);
    if (mode) await restart(mode);
    return;
  }
  if (!(await confirmIdle())) return;
  const result = await restart('');
  // 读状态到提交之间可能新增请求，由后端再次检查，交还用户决定。
  if (result.needs_confirm) {
    const mode = await chooseRequests(result.active_requests);
    if (mode) await restart(mode);
  }
};
