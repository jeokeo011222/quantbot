// 用户友好的错误提示映射
// 将技术性错误信息转换为用户可理解的提示

export interface FriendlyError {
  title: string
  message: string
  suggestion?: string
  level: 'error' | 'warning' | 'info'
}

// 错误关键词到友好提示的映射表
const errorMap: Record<string, FriendlyError> = {
  'Screener service not initialized': {
    title: '选股引擎未启动',
    message: '选股引擎服务尚未初始化，无法执行选股操作',
    suggestion: '请重启应用或检查系统配置',
    level: 'error',
  },
  'Portfolio engine not initialized': {
    title: '组合引擎未启动',
    message: '投资组合引擎尚未初始化',
    suggestion: '请重启应用以恢复功能',
    level: 'error',
  },
  'AI engine not initialized': {
    title: 'AI 引擎未启动',
    message: 'AI 分析引擎尚未初始化',
    suggestion: '请检查 AI 配置或重启应用',
    level: 'error',
  },
  '连接失败': {
    title: '网络连接失败',
    message: '无法连接到后端服务',
    suggestion: '请检查网络连接是否正常，然后重试',
    level: 'error',
  },
  'timeout': {
    title: '请求超时',
    message: '请求处理时间过长，可能网络不稳定',
    suggestion: '请稍后重试，如果持续出现请检查网络',
    level: 'warning',
  },
  'context deadline exceeded': {
    title: '操作超时',
    message: '操作执行时间过长',
    suggestion: '请检查数据源连接状态',
    level: 'warning',
  },
  '数据源': {
    title: '数据源连接问题',
    message: '数据获取出现异常',
    suggestion: '请在「设置」中检查数据源配置',
    level: 'warning',
  },
  'API': {
    title: 'API 调用异常',
    message: '接口调用出现错误',
    suggestion: '请检查 API Key 和网络连接',
    level: 'error',
  },
  '权限': {
    title: '权限不足',
    message: '当前版本不支持此功能',
    suggestion: '请升级到专业版或企业版以使用此功能',
    level: 'warning',
  },
  '配置': {
    title: '配置缺失',
    message: '必要的配置项未设置',
    suggestion: '请在「设置」页面完成相关配置',
    level: 'warning',
  },
  '风控': {
    title: '风控检查未通过',
    message: '当前操作违反了风控规则',
    suggestion: '请调整交易参数后重试',
    level: 'error',
  },
  '数据库': {
    title: '数据库连接异常',
    message: '无法访问本地数据库',
    suggestion: '请检查磁盘空间和数据库文件完整性',
    level: 'error',
  },
  'DuckDB': {
    title: '时序数据库异常',
    message: '股票数据存储服务出现问题',
    suggestion: '请重启应用以修复',
    level: 'error',
  },
  '行情数据库': {
    title: '行情数据缺失',
    message: '行情数据库文件未找到或未挂载',
    suggestion: '请确认 stock.duckdb 位于程序的 data 目录下，然后重启应用',
    level: 'error',
  },
  '选股引擎未初始化': {
    title: '选股引擎未启动',
    message: '选股引擎服务尚未初始化',
    suggestion: '请重启应用',
    level: 'error',
  },
  '获取股票列表失败': {
    title: '股票列表获取失败',
    message: '无法从数据库获取股票列表',
    suggestion: '请确认行情数据库文件完整，然后重启应用',
    level: 'error',
  },
  '股票池为空': {
    title: '股票池为空',
    message: '数据库中没有符合条件的股票数据',
    suggestion: '请确认行情数据库中包含股票基础信息和行情数据',
    level: 'error',
  },
  '选股引擎执行失败': {
    title: '选股执行失败',
    message: '选股引擎在执行过程中出现异常',
    suggestion: '请检查行情数据是否完整，或稍后重试',
    level: 'error',
  },
  '批量查询因子数据失败': {
    title: '因子数据查询失败',
    message: '批量获取股票行情数据失败',
    suggestion: '请确认行情数据库中包含足够的历史行情数据',
    level: 'error',
  },
  '所有批次数据获取失败': {
    title: '数据获取全部失败',
    message: '所有股票的行情数据获取均失败',
    suggestion: '请确认行情数据库文件完整且未损坏',
    level: 'error',
  },
  'TASK_BLOCKED': {
    title: '任务被数据完整性阻断',
    message: '行情数据缺失、过期或不完整，系统已停止任务执行',
    suggestion: '请先同步最新行情数据（数据维护 → 股票时序数据维护），再重新执行',
    level: 'error',
  },
  '数据已过期': {
    title: '行情数据已过期',
    message: '最新行情数据距今超过10天，禁止生成新的股票池',
    suggestion: '请先同步最新行情数据（数据维护 → 股票时序数据维护），再重新选股',
    level: 'warning',
  },
}

// 默认错误提示
const defaultError: FriendlyError = {
  title: '操作失败',
  message: '操作执行过程中出现了意外错误',
  suggestion: '请稍后重试，如果问题持续存在请联系技术支持',
  level: 'error',
}

/**
 * 将技术错误信息转换为用户友好提示
 */
export function toFriendlyError(error: unknown): FriendlyError {
  const message = extractErrorMessage(error)

  // 按优先级匹配：先匹配具体错误，再匹配通用关键词
  for (const [keyword, friendly] of Object.entries(errorMap)) {
    if (message.toLowerCase().includes(keyword.toLowerCase())) {
      return friendly
    }
  }

  return {
    ...defaultError,
    message: defaultError.message,
  }
}

/**
 * 从各种错误类型中提取信息
 */
function extractErrorMessage(error: unknown): string {
  if (error === null || error === undefined) {
    return ''
  }
  if (typeof error === 'string') {
    return error
  }
  if (error instanceof Error) {
    return error.message
  }
  if (typeof error === 'object') {
    const e = error as Record<string, unknown>
    if (typeof e.message === 'string') return e.message
    if (typeof e.Error === 'string') return e.Error
    if (typeof e.error === 'string') return e.error
    if (typeof e.msg === 'string') return e.msg
    if (typeof e.ErrorMessage === 'string') return e.ErrorMessage
  }
  return ''
}

/**
 * 格式化错误为 Toast 消息
 */
export function formatErrorForToast(error: unknown): { type: 'error' | 'warning' | 'info'; text: string } {
  const friendly = toFriendlyError(error)
  return {
    type: friendly.level === 'info' ? 'info' : friendly.level === 'warning' ? 'warning' : 'error',
    text: friendly.message,
  }
}

/**
 * 检查是否为网络/连接类错误
 */
export function isNetworkError(error: unknown): boolean {
  const msg = extractErrorMessage(error).toLowerCase()
  return msg.includes('connection') || msg.includes('timeout') || msg.includes('network') || msg.includes('econnrefused')
}

/**
 * 检查是否为版本权限错误
 */
export function isTierError(error: unknown): boolean {
  const msg = extractErrorMessage(error).toLowerCase()
  return msg.includes('tier') || msg.includes('upgrade') || msg.includes('permission') || msg.includes('unauthorized')
}
