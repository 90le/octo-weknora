export const runtimeStatusMessages = {
  zh: {
    title: '运行状态', im: 'IM 处理', waiting: '等待', active: '活动', rejected: '拒绝', timedOut: '等待超时', processScope: '当前服务进程；拒绝和超时为启动以来累计',
    sandbox: 'Docker 沙箱', occupancy: '占用 / 容量', config: '当前工作区配置', selectConfig: '请选择配置',
    configScope: '仅统计所选配置，容量未知时显示未知', configUnavailable: '配置列表暂不可用', noConfig: '当前工作区没有 Docker 配置', unknown: '未知',
    preparationTitle: 'IM 沙箱准备失败', preparationScope: '当前服务进程启动以来的实际失败次数', noFailures: '暂无失败记录',
    stages: { unknown: '其他阶段', pin: '固定配置', configuration: '读取配置', skills: '准备技能', policy: '准备策略', output_limit: '输出限制', reconcile: '协调沙箱' },
    preparationReasons: { unknown: '其他原因', unavailable: '不可用', timeout: '超时', denied: '无权限' },
    failures: { not_selected: '请选择配置', timeout: '查询超时', busy: '查询繁忙，请稍后重试', unavailable: '暂不可用', authentication: '身份验证失败', capacity: '容量不足', unsupported: '不支持查询', invalid_request: '查询参数无效' },
  },
  en: {
    title: 'Runtime status', im: 'IM processing', waiting: 'Waiting', active: 'Active', rejected: 'Rejected', timedOut: 'Timed out', processScope: 'Current process; rejected and timed out are totals since startup',
    sandbox: 'Docker sandbox', occupancy: 'In use / capacity', config: 'Current workspace configuration', selectConfig: 'Select a configuration',
    configScope: 'Only the selected configuration is counted; unknown capacity is shown as unknown', configUnavailable: 'Configurations unavailable', noConfig: 'No Docker configurations in this workspace', unknown: 'Unknown',
    preparationTitle: 'IM sandbox preparation failures', preparationScope: 'Actual failures since this service process started', noFailures: 'No failures recorded',
    stages: { unknown: 'Other stage', pin: 'Pin configuration', configuration: 'Read configuration', skills: 'Prepare skills', policy: 'Prepare policy', output_limit: 'Output limit', reconcile: 'Reconcile sandbox' },
    preparationReasons: { unknown: 'Other reason', unavailable: 'Unavailable', timeout: 'Timeout', denied: 'Access denied' },
    failures: { not_selected: 'Select a configuration', timeout: 'Query timed out', busy: 'Query busy; try again later', unavailable: 'Unavailable', authentication: 'Authentication failed', capacity: 'Capacity exceeded', unsupported: 'Query not supported', invalid_request: 'Invalid query' },
  },
} as const
