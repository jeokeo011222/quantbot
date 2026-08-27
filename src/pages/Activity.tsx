import { useState, useEffect, useRef, useCallback } from 'react'
import {
  Clock,
  User,
  Cpu,
  Shield,
  Zap,
  AlertTriangle,
  RefreshCw,
  Filter,
  Activity as ActivityIcon,
  Database,
  GitBranch,
  CheckCircle,
} from 'lucide-react'
import { AgentRole, AGENT_ROLE_NAMES } from '../types'
import { useAppStore } from '../store/appStore'
import { useToastStore } from '../store/toastStore'
import { fetchIndexData, type MarketIndex } from '../services/stockData'
import { getCurrentPhase as getMarketPhase, phaseLabels, MarketPhase } from '../utils/marketPhase'
import { GetConfig, GetTaskSchedulerStatus } from '../../wailsjs/go/main/App'

type ActivityType = 'research' | 'decision' | 'risk_check' | 'execution' | 'alert'

interface TodayTrade {
  trade_id: string
  side: string
  instrument_id: string
  quantity: number
  price: number
  gross_amount: number
  net_amount: number
  trade_date: string
}

// 将后端 Team Activity 转换为活动记录
interface TeamActivityItem {
  agent_role: string
  activity_type: string
  title: string
  message: string
  timestamp: string
}

const convertTeamActivityToActivity = (item: TeamActivityItem): ActivityItem => {
  let type: ActivityType = 'research'
  const titleLower = (item.title || '').toLowerCase()
  const activityTypeLower = (item.activity_type || '').toLowerCase()
  const rawMessage = item.message
  const isNullMessage = rawMessage === null || rawMessage === undefined || rawMessage === 'null' || rawMessage === 'undefined' || rawMessage === ''
  const msgLower = isNullMessage ? '' : rawMessage!.toLowerCase()

  if (activityTypeLower.includes('buy') || activityTypeLower.includes('sell') || activityTypeLower.includes('trade') ||
      titleLower.includes('买入') || titleLower.includes('卖出') || titleLower.includes('交易') ||
      msgLower.includes('买入') || msgLower.includes('卖出')) {
    type = 'execution'
  } else if (activityTypeLower.includes('risk') || titleLower.includes('风险') || titleLower.includes('风控')) {
    type = 'risk_check'
  } else if (activityTypeLower.includes('decision') || titleLower.includes('决策')) {
    type = 'decision'
  } else if (activityTypeLower.includes('alert') || titleLower.includes('警报') || titleLower.includes('异常')) {
    type = 'alert'
  }

  let time = ''
  if (item.timestamp) {
    time = item.timestamp.split('T')[1]?.substring(0, 8) || item.timestamp.split(' ')[1]?.substring(0, 8) || ''
  } else {
    time = formatTime(new Date())
  }

  let description = item.title || ''
  if (isNullMessage && description === 'null') {
    description = ''
  }
  if (!isNullMessage) {
    try {
      const msgObj = JSON.parse(rawMessage!)
      if (msgObj.stock && msgObj.action) {
        description = `${msgObj.action} ${msgObj.stock}${msgObj.quantity ? ` × ${msgObj.quantity}` : ''}${msgObj.price ? ` @ ${msgObj.price}` : ''}`
      } else if (msgObj.action) {
        description = `${msgObj.action} ${msgObj.stock || ''}`
      }
    } catch {
      if (rawMessage!.length < 100) {
        description = rawMessage!
      }
    }
  }

  const agentRole = ((item.agent_role || '').toUpperCase() || 'QUANT') as AgentRole

  return {
    id: `activity-${item.timestamp}-${item.agent_role}-${type}`,
    time,
    agentRole,
    type,
    description,
  }
}

// 将交易记录转换为活动记录（预留功能）
const _convertTradeToActivity = (trade: TodayTrade): ActivityItem => {
  const isBuy = trade.side === 'BUY'
  const action = isBuy ? '买入' : '卖出'

  let time = ''
  if (trade.trade_date) {
    time = trade.trade_date.split('T')[1]?.substring(0, 8) || trade.trade_date.split(' ')[1]?.substring(0, 8) || ''
  } else {
    time = formatTime(new Date())
  }

  return {
    id: `trade-${trade.trade_id}`,
    time,
    agentRole: 'CIO' as AgentRole,
    type: 'execution' as ActivityType,
    description: `${action} ${trade.instrument_id} × ${trade.quantity} @ ¥${trade.price.toFixed(2)}`,
  }
}

interface ActivityItem {
  id: string
  time: string
  agentRole: AgentRole
  type: ActivityType
  description: string
  subDescription?: string
  detail?: {
    taskName?: string
    summary?: string
    details?: string
    deliverable?: string
    errors?: string
    duration?: number
    startTime?: string | null
    endTime?: string | null
    status?: string
  }
}

const roleIconMap: Record<AgentRole, React.ReactNode> = {
  CIO: <Shield className="w-4 h-4 text-brand-500" />,
  PLANNER: <User className="w-4 h-4 text-purple-500" />,
  QUANT: <Cpu className="w-4 h-4 text-cyan-500" />,
  RISK: <AlertTriangle className="w-4 h-4 text-yellow-500" />,
  TRADER: <Zap className="w-4 h-4 text-green-500" />,
}

const activityTypeConfig: Record<
  ActivityType,
  { label: string; color: string; dot: string; icon: React.ReactNode }
> = {
  research: {
    label: '研究分析',
    color: 'text-cyan-600 dark:text-cyan-400',
    dot: 'bg-cyan-500',
    icon: <Cpu className="w-3.5 h-3.5" />,
  },
  decision: {
    label: '决策',
    color: 'text-brand-600 dark:text-brand-400',
    dot: 'bg-brand-500',
    icon: <Shield className="w-3.5 h-3.5" />,
  },
  risk_check: {
    label: '风控检查',
    color: 'text-yellow-600 dark:text-yellow-400',
    dot: 'bg-yellow-500',
    icon: <AlertTriangle className="w-3.5 h-3.5" />,
  },
  execution: {
    label: '执行交易',
    color: 'text-green-600 dark:text-green-400',
    dot: 'bg-green-500',
    icon: <Zap className="w-3.5 h-3.5" />,
  },
  alert: {
    label: '警报',
    color: 'text-red-600 dark:text-red-400',
    dot: 'bg-red-500',
    icon: <AlertTriangle className="w-3.5 h-3.5" />,
  },
}

const filterOptions: { key: 'all' | AgentRole; label: string }[] = [
  { key: 'all', label: '全部' },
  { key: 'CIO', label: 'CIO' },
  { key: 'PLANNER', label: 'Planner' },
  { key: 'QUANT', label: 'Quant' },
  { key: 'RISK', label: 'Risk' },
  { key: 'TRADER', label: 'Trader' },
]

const formatTime = (date: Date) => {
  return `${String(date.getHours()).padStart(2, '0')}:${String(date.getMinutes()).padStart(2, '0')}:${String(date.getSeconds()).padStart(2, '0')}`
}

const getLastTradingCloseTime = (): Date => {
  const now = new Date()
  const day = now.getDay()
  const minutes = now.getHours() * 60 + now.getMinutes()

  if (day === 0 || day === 6) {
    const daysBack = day === 0 ? 2 : 1
    const lastFriday = new Date(now)
    lastFriday.setDate(now.getDate() - daysBack)
    lastFriday.setHours(15, 0, 0, 0)
    return lastFriday
  }

  if (minutes >= 15 * 60) {
    const close = new Date(now)
    close.setHours(15, 0, 0, 0)
    return close
  }

  if (minutes >= 13 * 60 && minutes < 15 * 60) {
    return now
  }

  if (minutes >= 11 * 60 + 30 && minutes < 13 * 60) {
    const close = new Date(now)
    close.setHours(11, 30, 0, 0)
    return close
  }

  if (day === 1 && minutes < 9 * 60 + 30) {
    const lastFriday = new Date(now)
    lastFriday.setDate(now.getDate() - 3)
    lastFriday.setHours(15, 0, 0, 0)
    return lastFriday
  }

  const lastClose = new Date(now)
  lastClose.setDate(now.getDate() - 1)
  lastClose.setHours(15, 0, 0, 0)
  return lastClose
}

const isTradingTime = (): boolean => {
  const phase = getMarketPhase()
  return phase !== 'CLOSED'
}

interface PositionInfo {
  code: string
  name: string
}

// 透明度数据已从后端 GetTransparencyData API 获取

const getAgentWorkDetailsFromAPI = async (): Promise<Record<AgentRole, { state: string; currentTask: string; progress: number; todayTasks: string[] }> | null> => {
  try {
    const App: any = (window as any)['go']?.['main']?.['App']
    if (!App || typeof App.GetAgentWorkDetails !== 'function') {
      return null
    }

    const result = await App.GetAgentWorkDetails()
    if (result && result.details && Array.isArray(result.details)) {
      const detailsMap: Record<string, { state: string; currentTask: string; progress: number; todayTasks: string[] }> = {}
      for (const item of result.details) {
        const role = (item.role || '').toUpperCase()
        if (role) {
          detailsMap[role] = {
            state: item.state || '未知',
            currentTask: item.current_task || '无任务',
            progress: item.progress || 0,
            todayTasks: item.today_tasks || [],
          }
        }
      }
      return detailsMap as Record<AgentRole, { state: string; currentTask: string; progress: number; todayTasks: string[] }>
    }
    return null
  } catch {
    return null
  }
}

const getConfigForPhase = (phase: MarketPhase): { types: ActivityType[]; roles: AgentRole[]; descriptions: Record<ActivityType, string[]> } => {
  const preOpenDescriptions: Record<ActivityType, string[]> = {
    research: [
      '分析宏观经济指标影响...',
      '运行多因子模型扫描...',
      '计算因子衰减率...',
      '行业景气度排序完成...',
    ],
    decision: [
      'CIO 主持投资决策委员会...',
      '审议今日投资策略...',
      '批准 Alpha 策略...',
      '风险调整后收益评估...',
      '形成最终交易指令...',
    ],
    risk_check: [
      '盘前风险扫描...',
      '验证止损止盈设置...',
      '隔夜持仓风险评估...',
    ],
    execution: [],
    alert: [],
  }

  const intradayDescriptions: Record<ActivityType, string[]> = {
    research: [
      '实时因子监控...',
      '盘中板块轮动分析...',
      '持仓股技术指标分析...',
    ],
    decision: [],
    risk_check: [
      '实时监控 VaR 指标...',
      '审查持仓集中度...',
      '盘中流动性检查...',
      '波动率监控...',
    ],
    execution: [
      '盘中成交确认...',
      '限价订单执行...',
      '持仓股价格监控...',
      '分批调仓执行...',
    ],
    alert: [
      '持仓股触发预警...',
      '行业突发利空扫描...',
      '波动率骤升预警...',
      '大单异动监控...',
    ],
  }

  const postMarketDescriptions: Record<ActivityType, string[]> = {
    research: [
      '盘后复盘：因子归因分析...',
      '生成每日策略回测报告...',
      '当日交易绩效分析...',
    ],
    decision: [
      'CIO 主持盘后复盘会议...',
      '当日投资决策回顾...',
      '生成次日投资计划...',
    ],
    risk_check: [
      '盘后风险报告生成...',
      '当日VaR计算...',
      '持仓风险总结...',
    ],
    execution: [
      '尾盘调仓...',
      '当日成交核对...',
      '交易对账完成...',
    ],
    alert: [
      '盘后风险预警扫描...',
    ],
  }

  const reviewDescriptions: Record<ActivityType, string[]> = {
    research: [
      '深度因子归因分析...',
      '多周期因子对比...',
      '策略有效性验证...',
    ],
    decision: [
      'CIO 主持深度复盘...',
      '策略有效性评估...',
      '投资纪律检查...',
    ],
    risk_check: [
      '周度风险报告生成...',
      '压力测试执行...',
    ],
    execution: [],
    alert: [],
  }

  switch (phase) {
    case 'PRE_OPEN':
      return { types: ['decision', 'research', 'risk_check'] as ActivityType[], roles: ['CIO', 'QUANT', 'RISK', 'PLANNER'] as AgentRole[], descriptions: preOpenDescriptions }
    case 'MORNING_SESSION':
    case 'AFTERNOON_SESSION':
      return { types: ['execution', 'risk_check', 'alert', 'research'] as ActivityType[], roles: ['TRADER', 'RISK', 'QUANT'] as AgentRole[], descriptions: intradayDescriptions }
    case 'LUNCH':
      return { types: ['risk_check', 'research'] as ActivityType[], roles: ['RISK', 'QUANT', 'CIO'] as AgentRole[], descriptions: intradayDescriptions }
    case 'POST_MARKET':
      return { types: ['execution', 'risk_check', 'research', 'decision'] as ActivityType[], roles: ['TRADER', 'RISK', 'QUANT', 'CIO', 'PLANNER'] as AgentRole[], descriptions: postMarketDescriptions }
    case 'REVIEW':
      return { types: ['research', 'decision', 'risk_check'] as ActivityType[], roles: ['QUANT', 'CIO', 'PLANNER', 'RISK'] as AgentRole[], descriptions: reviewDescriptions }
    case 'CLOSED':
    default:
      return { types: ['research'] as ActivityType[], roles: ['QUANT'] as AgentRole[], descriptions: reviewDescriptions }
  }
}

// 生成新活动（预留功能）
const _generateNewActivity = (phase: MarketPhase, positions: PositionInfo[]): ActivityItem => {
  const { types, roles, descriptions } = getConfigForPhase(phase)
  
  // 过滤掉没有描述的类型
  const validTypes = types.filter(t => descriptions[t] && descriptions[t].length > 0)
  const validRoles: AgentRole[] = roles.length > 0 ? roles : ['QUANT']
  
  const type: ActivityType = validTypes.length > 0 
    ? validTypes[Math.floor(Math.random() * validTypes.length)] 
    : 'research'
  const role: AgentRole = validRoles[Math.floor(Math.random() * validRoles.length)]
  
  const descList = descriptions[type] || ['系统运行中...']
  let description = descList[Math.floor(Math.random() * descList.length)]

  // 使用真实持仓数据，所有类型都可能涉及具体持仓股票
  if (positions.length > 0) {
    const stock = positions[Math.floor(Math.random() * positions.length)]
    // 50%概率显示具体股票信息
    if (Math.random() > 0.5) {
      description = `${description}（${stock.code} ${stock.name}）`
    }
  }

  const isLive = phase === 'MORNING_SESSION' || phase === 'AFTERNOON_SESSION' || phase === 'PRE_OPEN' || phase === 'POST_MARKET'
  const timestamp = isLive ? new Date() : getLastTradingCloseTime()

  return {
    id: `activity-${Date.now()}-${Math.random().toString(36).slice(2, 7)}`,
    time: formatTime(timestamp),
    agentRole: role,
    type,
    description,
  }
}

type TransparencyTab = 'dataRead' | 'algorithms' | 'decisions'

interface TaskLogItem {
  id: number
  task_date: string
  task_phase: string
  agent_role: string
  task_name: string
  task_order: number
  status: string
  start_time: string | null
  end_time: string | null
  duration_ms: number
  deliverable_type: string
  deliverable_name: string
  deliverable_data: string
  summary: string
  details: string
  errors: string
  created_at: string
}

// 将 TaskLogItem 转换为 ActivityItem
const convertTaskLogToActivity = (task: TaskLogItem): ActivityItem => {
  // 确定活动类型
  let type: ActivityType = 'research'
  const taskNameLower = task.task_name.toLowerCase()
  const deliverableType = task.deliverable_type?.toLowerCase() || ''
  const summaryLower = task.summary?.toLowerCase() || ''
  const detailsLower = task.details?.toLowerCase() || ''

  let agentRole = (task.agent_role?.toUpperCase() || 'QUANT') as AgentRole

  if (taskNameLower.includes('trade') || taskNameLower.includes('交易') || taskNameLower.includes('execute') || deliverableType.includes('trade') || summaryLower.includes('买入') || summaryLower.includes('卖出')) {
    type = 'execution'
  } else if (taskNameLower.includes('risk') || taskNameLower.includes('风控') || taskNameLower.includes('check')) {
    type = 'risk_check'
  } else if (taskNameLower.includes('decision') || taskNameLower.includes('决策') || taskNameLower.includes('plan')) {
    type = 'decision'
  } else if (taskNameLower.includes('alert') || taskNameLower.includes('警报') || task.status === 'FAILED') {
    type = 'alert'
  }

  // 确定时间
  let time = ''
  if (task.end_time) {
    time = task.end_time.split('T')[1]?.substring(0, 8) || task.end_time.split(' ')[1]?.substring(0, 8) || ''
  } else if (task.start_time) {
    time = task.start_time.split('T')[1]?.substring(0, 8) || task.start_time.split(' ')[1]?.substring(0, 8) || ''
  } else {
    time = formatTime(new Date())
  }

  // 状态标签
  const statusLabel = task.status === 'RUNNING' ? '[执行中] ' : task.status === 'FAILED' ? '[失败] ' : task.status === 'COMPLETED' ? '[已完成] ' : ''
  // 角色标签
  const agentKey = (task.agent_role?.toUpperCase() || 'QUANT') as AgentRole
  const agentLabel = AGENT_ROLE_NAMES[agentKey] ? `[${AGENT_ROLE_NAMES[agentKey]}] ` : (task.agent_role ? `[${task.agent_role}] ` : '')

  // 组装主描述：优先详细摘要，其次带名称，兜底任务名
  let description = task.task_name
  if (task.summary) {
    description = task.summary
  } else if (task.deliverable_name) {
    description = `${task.task_name} - ${task.deliverable_name}`
  }

  // 追加：耗时（点击时打开弹窗显示详情，时间标识保留）
  const durationText = task.duration_ms ? ` · 耗时${(task.duration_ms / 1000).toFixed(1)}s` : ''
  // 补充说明：展示阶段、交付物、或失败原因（增加透明度）
  const phaseMap: Record<string, string> = {
    PRE_MARKET: '盘前',
    IN_MARKET: '盘中',
    POST_MARKET: '盘后',
    REVIEW: '复盘',
  }
  let subDescription = ''
  if (task.status === 'FAILED' && task.errors) {
    subDescription = `失败原因：${task.errors.slice(0, 140)}`
  } else if (task.deliverable_name && task.deliverable_name !== task.task_name) {
    subDescription = `交付物：${task.deliverable_name}`
  } else if (task.task_phase) {
    const ph = phaseMap[task.task_phase] || task.task_phase
    subDescription = `${ph}阶段任务${task.task_order ? `（第${task.task_order}项）` : ''}`
  }

  return {
    id: `task-${task.id}`,
    time,
    agentRole,
    type,
    description: `${statusLabel}${agentLabel}${description}${durationText}`,
    subDescription,
    detail: {
      taskName: task.task_name,
      summary: task.summary,
      details: task.details,
      deliverable: task.deliverable_name,
      errors: task.errors,
      duration: task.duration_ms,
      startTime: task.start_time,
      endTime: task.end_time,
      status: task.status,
    },
  }
}

export default function Activity() {
  const [activities, setActivities] = useState<ActivityItem[]>([])
  const [activeFilter, setActiveFilter] = useState<'all' | AgentRole>('all')
  const [lastRefresh, setLastRefresh] = useState<Date>(new Date())
  const [selectedAgent, setSelectedAgent] = useState<AgentRole>('CIO')
  const [showTransparency, setShowTransparency] = useState(false)
  const [transparencyTab, setTransparencyTab] = useState<TransparencyTab>('dataRead')
  const [indices, setIndices] = useState<MarketIndex[]>([])
  const [indicesUpdatedAt, setIndicesUpdatedAt] = useState<Date>(new Date())
  const [hasPositions, setHasPositions] = useState<boolean>(false)
  const [positionsCount, setPositionsCount] = useState<number>(0)
  const [positions, setPositions] = useState<PositionInfo[]>([])
  const feedRef = useRef<HTMLDivElement>(null)
  const pendingAuditRef = useRef<ActivityItem[]>([])
  const auditTimerRef = useRef<ReturnType<typeof setInterval> | null>(null)
  // 实时指数行情失败后的自动重试：失败弹窗警示一次，随后每 30 秒自动重试直至恢复
  const indicesRetryTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null)
  const indicesFailNotifiedRef = useRef(false)

  const [activitiesSource, setActivitiesSource] = useState<'real' | 'error' | 'loading'>('loading')
  const [, setRealActivitiesLoading] = useState(false)

  // 任务调度器状态
  const [schedulerStatus, setSchedulerStatus] = useState<{ is_running: boolean; current_phase: string; executor_available: boolean } | null>(null)

  // 真实智能体工作详情（从后端API加载）
  const [realAgentWorkDetails, setRealAgentWorkDetails] = useState<Record<string, { state: string; currentTask: string; progress: number; todayTasks: string[] } | null>>({})
  const [agentWorkDetailsLoading, setAgentWorkDetailsLoading] = useState(false)
  const [agentWorkDetailsSource, setAgentWorkDetailsSource] = useState<'real' | 'error' | 'loading'>('loading')
  const [agentWorkDetailsError, setAgentWorkDetailsError] = useState<string | null>(null)

  // 透明度数据相关状态
  const [transparencyData, setTransparencyData] = useState<{
    data_sources: Array<{ source: string; status: string; items: string[]; read_time: string }>;
    algorithms: Array<{ name: string; input: string; output: string; status: string; run_time: string; duration_ms: number }>;
    decisions: Array<{ step: number; agent: string; action: string; reason: string; data_used: string[]; timestamp: string }>;
    message?: string;
  } | null>(null)
  const [transparencyLoading, setTransparencyLoading] = useState(false)
  const [transparencyError, setTransparencyError] = useState<string | null>(null)

  const isAutoRefresh = useAppStore((s) => s.isAutoRefresh)
  const toggleAutoRefresh = useAppStore((s) => s.toggleAutoRefresh)
  const showError = useToastStore((s) => s.error)

  // 实时活动自动刷新间隔（分钟）：从后台配置读取（设置-通用），默认 5 分钟
  const [activityRefreshMinutes, setActivityRefreshMinutes] = useState(5)

  useEffect(() => {
    let cancelled = false
    ;(async () => {
      try {
        const cfg: any = await GetConfig()
        const minutes = Number(cfg?.activity_refresh_minutes)
        if (!cancelled && minutes >= 1 && minutes <= 60) {
          setActivityRefreshMinutes(minutes)
        } else if (!cancelled) {
          setActivityRefreshMinutes(5)
        }
      } catch {
        if (!cancelled) setActivityRefreshMinutes(5)
      }
    })()
    return () => {
      cancelled = true
    }
  }, [])

  // 指数行情加载：失败弹窗警示一次，随后每 30 秒自动重试直至恢复
  const loadIndices = useCallback(
    (opts?: { silent?: boolean }) => {
      if (indicesRetryTimerRef.current) {
        clearTimeout(indicesRetryTimerRef.current)
        indicesRetryTimerRef.current = null
      }
      fetchIndexData()
        .then((result) => {
          indicesFailNotifiedRef.current = false
          setIndices(result.data)
          setIndicesUpdatedAt(new Date())
        })
        .catch((err) => {
          if (!opts?.silent || !indicesFailNotifiedRef.current) {
            indicesFailNotifiedRef.current = true
            showError(
              '实时指数行情获取失败，将在30秒后自动重试',
              err instanceof Error ? err.message : String(err)
            )
          }
          indicesRetryTimerRef.current = setTimeout(() => loadIndices({ silent: true }), 30000)
        })
    },
    [showError]
  )
  const showWarning = useToastStore((s) => s.warning)

  // 加载透明度数据
  const loadTransparencyData = async () => {
    setTransparencyLoading(true)
    setTransparencyError(null)
    try {
      const App: any = (window as any)['go']?.['main']?.['App']
      if (!App || typeof App.GetTransparencyData !== 'function') {
        setTransparencyError('后端 API 未就绪')
        setTransparencyData(null)
        return
      }

      const result = await App.GetTransparencyData('')
      if (result && !result.error) {
        setTransparencyData({
          data_sources: result.data_sources || [],
          algorithms: result.algorithms || [],
          decisions: result.decisions || [],
          message: result.message,
        })
      } else {
        setTransparencyData({
          data_sources: [],
          algorithms: [],
          decisions: [],
          message: result?.message || '暂无透明度数据',
        })
      }
    } catch (err) {
      console.warn('Failed to load transparency data:', err)
      setTransparencyError('获取透明度数据失败')
      setTransparencyData(null)
    } finally {
      setTransparencyLoading(false)
    }
  }

  // 当显示透明度面板时加载数据
  useEffect(() => {
    if (showTransparency) {
      loadTransparencyData()
    }
  }, [showTransparency])

  // 从后端加载真实的 Agent 活动数据（团队活动 + 任务日志）
  const loadRealActivities = async () => {
    setRealActivitiesLoading(true)
    try {
      const App: any = (window as any)['go']?.['main']?.['App']
      if (!App || typeof App.GetTodayTeamActivity !== 'function') {
        setActivitiesSource('error')
        showError('活动数据加载失败', '无法连接后端活动服务，请检查应用状态')
        return
      }

      const allActivities: ActivityItem[] = []

      // 加载今日团队活动
      try {
        const teamResult = await App.GetTodayTeamActivity()
        if (teamResult && teamResult.activities && Array.isArray(teamResult.activities)) {
          const teamActivities: ActivityItem[] = teamResult.activities.map((item: any) => convertTeamActivityToActivity(item))
          allActivities.push(...teamActivities)
        }
      } catch (err) {
        const errMsg = err instanceof Error ? err.message : String(err)
        showWarning('团队活动加载异常', `部分活动数据加载失败: ${errMsg}`)
      }

      // 加载最新任务日志作为补充
      try {
        if (typeof App.GetLatestAgentTaskLogs === 'function') {
          const taskResult = await App.GetLatestAgentTaskLogs()
          if (taskResult && taskResult.logs && Array.isArray(taskResult.logs)) {
            const taskActivities: ActivityItem[] = taskResult.logs.map((task: TaskLogItem) => convertTaskLogToActivity(task))
            allActivities.push(...taskActivities)
          }
        }
      } catch (err) {
        const errMsg = err instanceof Error ? err.message : String(err)
        showWarning('任务日志加载异常', `部分日志数据加载失败: ${errMsg}`)
      }

      // API 调用成功，即使数据为空也标记为 real（空数据是合法状态）
      if (allActivities.length > 0) {
        // 去重并按时间倒序排列
        const seen = new Set<string>()
        const uniqueActivities = allActivities.filter(a => {
          if (seen.has(a.id)) return false
          seen.add(a.id)
          return true
        })
        uniqueActivities.sort((a, b) => b.time.localeCompare(a.time))
        setActivities(uniqueActivities.slice(0, 200))
      } else {
        setActivities([])
      }
      setActivitiesSource('real')
      setLastRefresh(new Date())
    } catch (err) {
      const errMsg = err instanceof Error ? err.message : String(err)
      console.error('Failed to load real activities:', err)
      setActivitiesSource('error')
      showError('实时活动加载失败', `无法加载活动数据: ${errMsg}`)
    } finally {
      setRealActivitiesLoading(false)
    }
  }

  // 从后端加载真实的 Agent 工作详情
  const loadAgentWorkDetails = async () => {
    setAgentWorkDetailsLoading(true)
    try {
      const App: any = (window as any)['go']?.['main']?.['App']
      if (!App || typeof App.GetAgentWorkDetails !== 'function') {
        setAgentWorkDetailsError('后端 API 未就绪，无法获取智能体工作详情')
        setAgentWorkDetailsSource('error')
        return
      }

      const result = await App.GetAgentWorkDetails()
      if (result && result.details && Array.isArray(result.details)) {
        const detailsMap: Record<string, { state: string; currentTask: string; progress: number; todayTasks: string[] }> = {}
        for (const item of result.details) {
          const role = (item.role || '').toUpperCase()
          if (role) {
            detailsMap[role] = {
              state: item.state || '未知',
              currentTask: item.current_task || '无任务',
              progress: item.progress || 0,
              todayTasks: item.today_tasks || [],
            }
          }
        }
        setRealAgentWorkDetails(detailsMap)
        setAgentWorkDetailsSource('real')
        setAgentWorkDetailsError(null)
      } else if (result && result.details) {
        // details 存在但可能为空数组，视为合法响应
        setRealAgentWorkDetails({})
        setAgentWorkDetailsSource('real')
        setAgentWorkDetailsError(null)
      } else {
        setAgentWorkDetailsError('返回数据格式错误')
        setAgentWorkDetailsSource('error')
      }
    } catch (err) {
      console.error('Failed to load agent work details:', err)
      setAgentWorkDetailsError(err instanceof Error ? err.message : String(err))
      setAgentWorkDetailsSource('error')
    } finally {
      setAgentWorkDetailsLoading(false)
    }
  }

  // 加载任务调度器状态
  const loadSchedulerStatus = async () => {
    try {
      const status = await GetTaskSchedulerStatus()
      if (status) {
        setSchedulerStatus({
          is_running: status.is_running || false,
          current_phase: status.current_phase || '',
          executor_available: status.executor_available || false,
        })
      }
    } catch {
      // 静默失败，不影响主要功能
    }
  }

  const phase = getMarketPhase()

  // 智能体工作详情：使用真实API数据，如果出错则显示错误状态
  const agentWorkDetails: Record<AgentRole, { state: string; currentTask: string; progress: number; todayTasks: string[] }> = {} as any
  ;(['CIO', 'PLANNER', 'QUANT', 'RISK', 'TRADER'] as AgentRole[]).forEach((role) => {
    if (agentWorkDetailsSource === 'loading') {
      agentWorkDetails[role] = {
        state: '加载中',
        currentTask: '正在获取任务状态...',
        progress: 0,
        todayTasks: [],
      }
    } else if (agentWorkDetailsSource === 'real' && realAgentWorkDetails[role]) {
      agentWorkDetails[role] = realAgentWorkDetails[role]!
    } else if (agentWorkDetailsSource === 'error') {
      agentWorkDetails[role] = {
        state: '错误',
        currentTask: agentWorkDetailsError || '数据加载失败',
        progress: 0,
        todayTasks: ['数据获取失败，请检查后端服务状态'],
      }
    } else {
      agentWorkDetails[role] = {
        state: '待机中',
        currentTask: '无任务',
        progress: 0,
        todayTasks: [],
      }
    }
  })
  const flushAuditActivities = async () => {
    const items = pendingAuditRef.current
    if (items.length === 0) return
    pendingAuditRef.current = []

    const App: any = (window as any)['go']?.['main']?.['App']
    if (!App || typeof App.LogLiveActivity !== 'function') return

    for (const item of items) {
      try {
        await App.LogLiveActivity(
          item.agentRole,
          item.type,
          item.description,
          JSON.stringify({
            time: item.time,
            id: item.id,
          })
        )
      } catch {
        // silently ignore audit log failures
      }
    }
  }

  const _logActivityToAudit = (item: ActivityItem) => {
    pendingAuditRef.current.push(item)
    if (pendingAuditRef.current.length >= 5) {
      flushAuditActivities()
    }
  }

  useEffect(() => {
    flushAuditActivities()
    auditTimerRef.current = setInterval(flushAuditActivities, 10000)
    return () => {
      if (auditTimerRef.current) {
        clearInterval(auditTimerRef.current)
      }
      flushAuditActivities()
    }
  }, [])

  useEffect(() => {
    loadIndices()
    // 获取持仓状态和持仓明细
    const App: any = (window as any)['go']?.['main']?.['App']
    if (App && typeof App.GetPortfolioState === 'function') {
      App.GetPortfolioState().then((state: any) => {
        const count = state?.positionsCount || 0
        setPositionsCount(count)
        setHasPositions(count > 0)
        // 提取持仓明细用于活动生成
        if (state?.positions && Array.isArray(state.positions)) {
          const posList: PositionInfo[] = state.positions.map((p: any) => ({
            code: p.code,
            name: p.stockName,
          }))
          setPositions(posList)
        }
      }).catch(() => {
        setHasPositions(false)
      })
    }
    // 加载真实的 Agent 活动数据和今日交易
    loadRealActivities()
    loadAgentWorkDetails()
    loadSchedulerStatus()
  }, [])

  useEffect(() => {
    if (!isAutoRefresh) return
    const interval = setInterval(() => {
      if (!isTradingTime()) {
        return
      }
      // 刷新真实的 Agent 活动数据
      loadRealActivities()
      loadAgentWorkDetails()
      loadSchedulerStatus()
      // 交易时段内同时刷新实时指数行情
      loadIndices({ silent: true })
    }, activityRefreshMinutes * 60 * 1000) // 按后台配置的分钟数刷新
    return () => clearInterval(interval)
  }, [isAutoRefresh, activityRefreshMinutes, loadIndices])

  // 组件卸载时清理指数自动重试定时器
  useEffect(() => {
    return () => {
      if (indicesRetryTimerRef.current) {
        clearTimeout(indicesRetryTimerRef.current)
        indicesRetryTimerRef.current = null
      }
    }
  }, [])

  const handleRefreshPositions = async () => {
    const App: any = (window as any)['go']?.['main']?.['App']
    if (App && typeof App.GetPortfolioState === 'function') {
      try {
        const state = await App.GetPortfolioState()
        const count = state?.positionsCount || 0
        setPositionsCount(count)
        setHasPositions(count > 0)
        // 更新持仓明细
        if (state?.positions && Array.isArray(state.positions)) {
          const posList: PositionInfo[] = state.positions.map((p: any) => ({
            code: p.code,
            name: p.stockName,
          }))
          setPositions(posList)
        } else {
          setPositions([])
        }
      } catch {
        // ignore
      }
    }
    // 同时刷新真实的 Agent 活动和交易数据
    loadRealActivities()
    loadAgentWorkDetails()
  }

  const filteredActivities =
    activeFilter === 'all'
      ? activities
      : activities.filter((a) => a.agentRole === activeFilter)

  const handleManualRefresh = () => {
    // 刷新真实的 Agent 活动数据和今日交易
    loadRealActivities()
    loadAgentWorkDetails()
  }

  const autoRefreshIntervalLabel = isAutoRefresh ? `${activityRefreshMinutes}分钟` : '已暂停'
  const isMarketOpen = isTradingTime()

  return (
    <div className="space-y-5">
      {/* 无持仓提示 */}
      {!hasPositions && (
        <div className="card p-6 border-2 border-yellow-200 dark:border-yellow-800 bg-yellow-50 dark:bg-yellow-900/20">
          <div className="flex items-start gap-4">
            <div className="w-12 h-12 rounded-full bg-yellow-100 dark:bg-yellow-900/40 flex items-center justify-center flex-shrink-0">
              <AlertTriangle className="w-6 h-6 text-yellow-600 dark:text-yellow-400" />
            </div>
            <div className="flex-1">
              <h3 className="text-lg font-semibold text-yellow-800 dark:text-yellow-200 mb-2">
                暂无持仓
              </h3>
              <p className="text-sm text-yellow-700 dark:text-yellow-300 mb-3">
                当前系统中没有任何持仓。AI 实时分析不会因"暂无持仓"而暂停：首席投资官（CIO）会依据是否存在投资方案，并结合当前市场状态，自动判断是否建仓（新建仓位）。
              </p>
              <ul className="text-sm text-yellow-600 dark:text-yellow-400 space-y-1 ml-4 mb-4">
                <li>• 已有投资方案：CIO 将结合市场判断决定是否自动建仓，并在下方实时活动中展示决策</li>
                <li>• 尚无投资方案：可先通过「投资规划」生成 AI 推荐的投资组合，供 CIO 决策</li>
                <li>• 也可通过「手动交易」直接买入股票，建立初始仓位</li>
              </ul>
              <div className="flex items-center gap-2 text-xs text-yellow-600 dark:text-yellow-400">
                <span className="px-2 py-1 bg-yellow-200 dark:bg-yellow-900/40 rounded">
                  当前持仓：{positionsCount} 只
                </span>
                <span className="px-2 py-1 bg-yellow-200 dark:bg-yellow-900/40 rounded">
                  AI实时分析：运行中（CIO待命，随时可建仓）
                </span>
              </div>
            </div>
          </div>
        </div>
      )}

      <header className="flex items-center justify-between">
        <div className="flex items-center gap-3">
          <div className="w-10 h-10 rounded-lg bg-gradient-to-br from-brand-500 to-cyan-400 flex items-center justify-center shadow-sm">
            <ActivityIcon className="w-5 h-5 text-white" />
          </div>
          <div>
            <h1 className="text-lg font-bold text-slate-800 dark:text-slate-100">
              实时活动 - AI 现场直播
            </h1>
            <p className="text-xs text-slate-500 dark:text-slate-400 flex items-center gap-2">
              <span>当前时段：{phaseLabels[phase]}</span>
              <span className={`px-1.5 py-0.5 rounded text-[10px] ${isMarketOpen ? 'bg-green-100 text-green-700 dark:bg-green-900/30 dark:text-green-400' : 'bg-slate-100 text-slate-500 dark:bg-slate-700 dark:text-slate-400'}`}>
                {isMarketOpen ? '活跃' : '休市'}
              </span>
            </p>
          </div>
        </div>

        <div className="flex items-center gap-2">
          <span
            className={`badge ${
              !isMarketOpen
                ? 'bg-slate-100 text-slate-500 dark:bg-slate-700 dark:text-slate-400'
                : isAutoRefresh
                ? 'bg-green-100 text-green-700 dark:bg-green-900/30 dark:text-green-400'
                : 'bg-slate-100 text-slate-600 dark:bg-slate-700 dark:text-slate-400'
            }`}
          >
            <RefreshCw
              className={`w-3 h-3 mr-1 ${isMarketOpen && isAutoRefresh ? 'animate-spin' : ''}`}
              style={{ animationDuration: '4s' }}
            />
            {!isMarketOpen ? '非交易时段' : isAutoRefresh ? `自动刷新 · ${autoRefreshIntervalLabel}` : '自动刷新已暂停'}
          </span>

          <button onClick={toggleAutoRefresh} className="btn-secondary text-xs py-1.5 px-3">
            {isAutoRefresh ? '暂停' : '启动'}
          </button>

          {/* 无持仓时显示刷新持仓状态按钮 */}
          {!hasPositions && (
            <button onClick={handleRefreshPositions} className="btn-primary text-xs py-1.5 px-3">
              <RefreshCw className="w-3.5 h-3.5 mr-1" />
              刷新持仓状态
            </button>
          )}

          <button onClick={handleManualRefresh} className="btn-secondary text-xs py-1.5 px-3 disabled:opacity-50 disabled:cursor-not-allowed" disabled={!hasPositions || positions.length === 0}>
            <RefreshCw className="w-3.5 h-3.5 mr-1" />
            立即刷新
          </button>

          <button
            onClick={() => setShowTransparency(!showTransparency)}
            className={`btn-secondary text-xs py-1.5 px-3 ${showTransparency ? 'bg-purple-100 text-purple-700' : ''}`}
          >
            <GitBranch className="w-3.5 h-3.5 mr-1" />
            AI 决策过程
          </button>
        </div>
      </header>

      {indices.length > 0 && (
        <div className="card p-4">
          <div className="flex items-center justify-between mb-3">
            <div className="flex items-center gap-2">
              <Database className="w-4 h-4 text-brand-500" />
              <h3 className="text-sm font-semibold text-slate-700 dark:text-slate-300">
                市场指数 · AI 读取的实时数据
              </h3>
            </div>
            <div className="flex items-center gap-2 text-xs text-slate-400 dark:text-slate-500">
              <span>更新时间: {indicesUpdatedAt.toLocaleTimeString('zh-CN')}</span>
              <button
                onClick={() => loadIndices()}
                className="p-1 rounded hover:bg-slate-100 dark:hover:bg-slate-800"
                title="刷新数据"
              >
                <RefreshCw className="w-3 h-3" />
              </button>
            </div>
          </div>
          <div className="grid grid-cols-3 gap-4">
            {indices.map((idx) => {
              const isUp = idx.changePercent >= 0
              return (
                <div key={idx.code} className="p-3 rounded-lg bg-slate-50 dark:bg-slate-900/50 border border-slate-100 dark:border-slate-800">
                  <div className="flex items-center justify-between mb-1">
                    <span className="text-xs text-slate-500 dark:text-slate-400">{idx.name}</span>
                    <span className="text-[10px] text-slate-400 dark:text-slate-600">{idx.code}</span>
                  </div>
                  <div className={`text-lg font-bold ${isUp ? 'text-red-500' : 'text-green-500'}`}>
                    {idx.current.toFixed(2)}
                  </div>
                  <div className={`text-xs ${isUp ? 'text-red-500' : 'text-green-500'}`}>
                    {isUp ? '+' : ''}{idx.change.toFixed(2)} ({isUp ? '+' : ''}{idx.changePercent.toFixed(2)}%)
                  </div>
                  <div className="mt-1 text-[10px] text-slate-400 dark:text-slate-600 flex items-center gap-1">
                    <span className="w-1.5 h-1.5 rounded-full bg-green-400"></span>
                    实时行情
                  </div>
                </div>
              )
            })}
          </div>
        </div>
      )}

      {/* Task Execution Flow - 任务执行流程可视化 */}
      <div className="card p-5">
        <div className="flex items-center justify-between mb-4">
          <div className="flex items-center gap-2">
            <GitBranch className="w-4 h-4 text-brand-500" />
            <h2 className="text-sm font-semibold text-slate-700 dark:text-slate-300">
              今日任务执行流程 · {phaseLabels[phase]}
            </h2>
          </div>
          <div className="flex items-center gap-3">
            {schedulerStatus && !schedulerStatus.executor_available && (
              <span className="inline-flex items-center gap-1 px-2 py-1 rounded bg-red-50 dark:bg-red-900/20 text-red-600 dark:text-red-400 text-xs">
                <AlertTriangle className="w-3 h-3" />
                执行器不可用
              </span>
            )}
            {schedulerStatus && schedulerStatus.is_running && schedulerStatus.executor_available && (
              <span className="inline-flex items-center gap-1 px-2 py-1 rounded bg-green-50 dark:bg-green-900/20 text-green-600 dark:text-green-400 text-xs">
                <CheckCircle className="w-3 h-3" />
                调度器运行中
              </span>
            )}
            <span className="text-xs text-slate-400 dark:text-slate-500">
              显示编排引擎中每个工作流的实时执行步骤
            </span>
          </div>
        </div>

        {/* Investment Pipeline Status - 权限链: PROPOSE→EVIDENCE→VETO→DECIDE→EXECUTE */}
        <div className="grid grid-cols-2 sm:grid-cols-3 lg:grid-cols-5 gap-2 mb-4">
          {[
            { role: 'PLANNER', label: 'Planner', step: '提出方案', permission: 'PROPOSE', icon: '🎯' },
            { role: 'QUANT', label: 'Quant', step: '提供证据', permission: 'EVIDENCE', icon: '🔍' },
            { role: 'RISK', label: 'Risk', step: '风险否决', permission: 'VETO', icon: '🛡️' },
            { role: 'CIO', label: 'CIO', step: '最终决策', permission: 'DECIDE', icon: '👑' },
            { role: 'TRADER', label: 'Trader', step: '执行交易', permission: 'EXECUTE', icon: '⚡' },
          ].map((step, idx) => {
            const d = agentWorkDetails[step.role as AgentRole]
            const isActive = d && (d.state === '工作中' || d.state === '执行中')
            const isWaiting = d && d.state === '等待中'
            const isCompleted = d && d.state === '已完成'
            return (
              <div
                key={idx}
                className={`p-3 rounded-lg border text-center ${
                  isActive
                    ? 'border-blue-400 bg-blue-50 dark:bg-blue-900/20 dark:border-blue-700'
                    : isWaiting
                    ? 'border-slate-200 dark:border-slate-700 bg-slate-50 dark:bg-slate-900/50'
                    : isCompleted
                    ? 'border-emerald-300 dark:border-emerald-700 bg-emerald-50 dark:bg-emerald-900/20'
                    : 'border-green-200 dark:border-green-800 bg-green-50 dark:bg-green-900/10'
                }`}
              >
                <div className="text-lg mb-1">{step.icon}</div>
                <div className="text-xs font-semibold text-slate-700 dark:text-slate-200">{step.label}</div>
                <div className="text-[10px] text-slate-500 dark:text-slate-400">{step.step}</div>
                {isActive && (
                  <div className="mt-1.5">
                    <div className="h-1 bg-slate-200 dark:bg-slate-700 rounded-full overflow-hidden">
                      <div className="h-full bg-blue-500 animate-pulse" style={{ width: `${d.progress}%` }} />
                    </div>
                    <div className="text-[10px] text-blue-600 dark:text-blue-400 mt-0.5">{d.progress}%</div>
                  </div>
                )}
                {isCompleted && (
                  <div className="mt-1 text-[10px] text-emerald-600 dark:text-emerald-400 flex items-center justify-center gap-0.5">
                    <CheckCircle className="w-3 h-3" />
                    <span>已完成</span>
                  </div>
                )}
                {!isActive && !isWaiting && !isCompleted && d?.state === '休息中' && (
                  <div className="text-[10px] text-yellow-500 mt-1">休息中</div>
                )}
              </div>
            )
          })}
        </div>

        {/* Active Workflow Steps Detail */}
        {(() => {
          const activeStates = ['工作中', '执行中', '监控中']
          const completedStates = ['已完成']
          const activeAgent = (Object.entries(agentWorkDetails) as [AgentRole, typeof agentWorkDetails[AgentRole]][])
            .find(([_, d]) => activeStates.includes(d.state))
          const allCompleted = (Object.values(agentWorkDetails) as typeof agentWorkDetails[AgentRole][])
            .every((d) => completedStates.includes(d.state))
          if (allCompleted) {
            return (
              <div className="text-center py-4">
                <div className="inline-flex items-center gap-2 px-4 py-2 rounded-full bg-emerald-50 dark:bg-emerald-900/20 border border-emerald-200 dark:border-emerald-700">
                  <CheckCircle className="w-5 h-5 text-emerald-500" />
                  <span className="text-sm font-semibold text-emerald-600 dark:text-emerald-400">今日所有任务已完成</span>
                </div>
                <div className="mt-2 text-xs text-slate-500 dark:text-slate-400">
                  所有Agent的今日任务编排已全部执行完毕，系统进入待命状态
                </div>
              </div>
            )
          }
          if (!activeAgent) {
            const phase = getMarketPhase()
            const isTradingPhase = ['MORNING_SESSION', 'AFTERNOON_SESSION', 'PRE_OPEN', 'PRE_MARKET'].includes(phase)
            const phaseMsg = isTradingPhase ? '当前无活跃任务 · 交易时段 Agent 正在监控市场' : '当前无活跃任务 · 非交易时段 Agent 自动进入休眠'
            return (
              <div className="text-center py-4 text-slate-500 dark:text-slate-400 text-xs">
                {phaseMsg}
              </div>
            )
          }
          const [role, d] = activeAgent
          const stateLabel = d.state === '监控中' ? '监控中' : '执行中'
          return (
            <div className="border border-slate-200 dark:border-slate-700 rounded-lg p-3 bg-slate-50 dark:bg-slate-900/30">
              <div className="flex items-center justify-between mb-2">
                <div className="flex items-center gap-2">
                  {roleIconMap[role]}
                  <span className="text-sm font-semibold text-slate-700 dark:text-slate-200">{AGENT_ROLE_NAMES[role]} · {stateLabel}</span>
                </div>
                <span className="text-xs text-blue-600 dark:text-blue-400">{d.progress}% 完成</span>
              </div>
              <div className="text-xs text-slate-600 dark:text-slate-400 mb-2">
                <span className="font-medium">{d.currentTask}</span>
              </div>
              <div className="h-2 bg-slate-200 dark:bg-slate-700 rounded-full overflow-hidden mb-2">
                <div className="h-full bg-gradient-to-r from-brand-500 to-cyan-400 transition-all" style={{ width: `${d.progress}%` }} />
              </div>
              <div className="space-y-1">
                {d.todayTasks.slice(0, 3).map((task, i) => (
                  <div key={i} className="flex items-center gap-2 text-xs">
                    <CheckCircle className="w-3 h-3 text-green-500" />
                    <span className="text-slate-600 dark:text-slate-300">{task}</span>
                  </div>
                ))}
              </div>
            </div>
          )
        })()}
      </div>

      {showTransparency && (
        <div className="card p-5">
          <div className="flex items-center gap-2 mb-4">
            <GitBranch className="w-4 h-4 text-purple-500" />
            <h2 className="text-sm font-semibold text-slate-700 dark:text-slate-300">
              AI 决策过程透明化 · 它读了什么？怎么决策的？
            </h2>
          </div>

          <div className="flex items-center gap-1 mb-4">
            {([
              { key: 'dataRead' as TransparencyTab, label: '📥 读取的数据', icon: Database },
              { key: 'algorithms' as TransparencyTab, label: '🧮 运行的算法', icon: Cpu },
              { key: 'decisions' as TransparencyTab, label: '🎯 决策链路', icon: GitBranch },
            ]).map((tab) => (
              <button
                key={tab.key}
                onClick={() => setTransparencyTab(tab.key)}
                className={`flex items-center gap-1.5 px-3 py-1.5 text-xs rounded-lg transition-colors ${
                  transparencyTab === tab.key
                    ? 'bg-purple-100 text-purple-700 dark:bg-purple-900/30 dark:text-purple-300 font-medium'
                    : 'text-slate-500 hover:bg-slate-100 dark:hover:bg-slate-700'
                }`}
              >
                <tab.icon className="w-3.5 h-3.5" />
                {tab.label}
              </button>
            ))}
          </div>

          {transparencyTab === 'dataRead' && (
            <div className="space-y-4">
              {transparencyLoading ? (
                <div className="text-center py-8">
                  <RefreshCw className="w-8 h-8 text-slate-400 animate-spin mx-auto mb-3" />
                  <p className="text-sm text-slate-500 dark:text-slate-400">加载中...</p>
                </div>
              ) : !transparencyData || transparencyData.data_sources.length === 0 ? (
                <div className="text-center py-8">
                  <AlertTriangle className="w-12 h-12 text-yellow-500 mx-auto mb-3" />
                  <p className="text-sm text-slate-500 dark:text-slate-400">
                    {transparencyData?.message || '暂无数据源记录'}
                  </p>
                  <p className="text-xs text-slate-400 mt-2">
                    执行智能体任务后，将在此展示 AI 决策前读取的真实数据源
                  </p>
                </div>
              ) : (
                <>
                  <div className="flex items-center justify-between mb-3">
                    <p className="text-xs text-slate-500 dark:text-slate-400">
                      AI 在做出投资决策前，从以下数据源读取了 {transparencyData.data_sources.length} 条记录
                    </p>
                    <button
                      onClick={loadTransparencyData}
                      className="text-xs text-purple-600 hover:text-purple-700 flex items-center gap-1"
                    >
                      <RefreshCw className="w-3 h-3" />
                      刷新
                    </button>
                  </div>
                  {transparencyData.data_sources.map((source, idx) => (
                    <div key={idx} className="border border-slate-200 dark:border-slate-700 rounded-lg p-4">
                      <div className="flex items-center justify-between mb-2">
                        <div className="flex items-center gap-2">
                          <Database className="w-4 h-4 text-brand-500" />
                          <span className="text-sm font-semibold text-slate-800 dark:text-slate-100">{source.source}</span>
                        </div>
                        <span className={`text-xs px-2 py-0.5 rounded ${
                          source.status === '成功' ? 'bg-green-100 text-green-700 dark:bg-green-900/30 dark:text-green-400' : 'bg-red-100 text-red-700 dark:bg-red-900/30 dark:text-red-400'
                        }`}>
                          {source.status}
                        </span>
                      </div>
                      <div className="flex flex-wrap gap-1.5">
                        {source.items.map((item, i) => (
                          <span key={i} className="text-xs px-2 py-0.5 rounded bg-slate-100 dark:bg-slate-700 text-slate-600 dark:text-slate-400 font-mono">
                            {item.length > 50 ? item.substring(0, 50) + '...' : item}
                          </span>
                        ))}
                      </div>
                      <p className="text-xs text-slate-400 mt-2">读取时间: {source.read_time}</p>
                    </div>
                  ))}
                </>
              )}
            </div>
          )}

          {transparencyTab === 'algorithms' && (
            <div className="space-y-3">
              {transparencyLoading ? (
                <div className="text-center py-8">
                  <RefreshCw className="w-8 h-8 text-slate-400 animate-spin mx-auto mb-3" />
                  <p className="text-sm text-slate-500 dark:text-slate-400">加载中...</p>
                </div>
              ) : !transparencyData || transparencyData.algorithms.length === 0 ? (
                <div className="text-center py-8">
                  <Cpu className="w-12 h-12 text-yellow-500 mx-auto mb-3" />
                  <p className="text-sm text-slate-500 dark:text-slate-400">
                    {transparencyData?.message || '暂无算法执行记录'}
                  </p>
                  <p className="text-xs text-slate-400 mt-2">
                    执行智能体任务后，将在此展示 AI 运行的算法模型
                  </p>
                </div>
              ) : (
                <>
                  <div className="flex items-center justify-between mb-3">
                    <p className="text-xs text-slate-500 dark:text-slate-400">
                      AI 运行了 {transparencyData.algorithms.length} 个算法/工具
                    </p>
                    <button
                      onClick={loadTransparencyData}
                      className="text-xs text-purple-600 hover:text-purple-700 flex items-center gap-1"
                    >
                      <RefreshCw className="w-3 h-3" />
                      刷新
                    </button>
                  </div>
                  <div className="grid grid-cols-2 gap-3">
                    {transparencyData.algorithms.map((algo, idx) => (
                      <div key={idx} className="border border-slate-200 dark:border-slate-700 rounded-lg p-4">
                        <div className="flex items-center justify-between mb-2">
                          <div className="flex items-center gap-2">
                            <Cpu className="w-4 h-4 text-cyan-500" />
                            <span className="text-sm font-semibold text-slate-800 dark:text-slate-100">{algo.name}</span>
                          </div>
                          <span className="text-xs text-slate-400">{algo.duration_ms}ms</span>
                        </div>
                        <div className="space-y-1.5 text-xs">
                          <div className="flex items-start gap-1.5">
                            <span className="text-slate-400 shrink-0">输入:</span>
                            <span className="text-slate-600 dark:text-slate-400 break-all">
                              {algo.input.length > 80 ? algo.input.substring(0, 80) + '...' : algo.input}
                            </span>
                          </div>
                          <div className="flex items-start gap-1.5">
                            <span className="text-slate-400 shrink-0">输出:</span>
                            <span className="text-slate-600 dark:text-slate-400 break-all">
                              {algo.output.length > 80 ? algo.output.substring(0, 80) + '...' : algo.output}
                            </span>
                          </div>
                          <div className="flex items-center justify-between">
                            <span className={`font-medium ${
                              algo.status === '成功' ? 'text-green-500' : 'text-red-500'
                            }`}>{algo.status}</span>
                            <span className="text-slate-400">{algo.run_time}</span>
                          </div>
                        </div>
                      </div>
                    ))}
                  </div>
                </>
              )}
            </div>
          )}

          {transparencyTab === 'decisions' && (
            <div className="space-y-3">
              {transparencyLoading ? (
                <div className="text-center py-8">
                  <RefreshCw className="w-8 h-8 text-slate-400 animate-spin mx-auto mb-3" />
                  <p className="text-sm text-slate-500 dark:text-slate-400">加载中...</p>
                </div>
              ) : !transparencyData || transparencyData.decisions.length === 0 ? (
                <div className="text-center py-8">
                  <GitBranch className="w-12 h-12 text-yellow-500 mx-auto mb-3" />
                  <p className="text-sm text-slate-500 dark:text-slate-400">
                    {transparencyData?.message || '暂无决策链路记录'}
                  </p>
                  <p className="text-xs text-slate-400 mt-2">
                    执行智能体任务后，将在此展示从数据到决策的完整链路
                  </p>
                </div>
              ) : (
                <>
                  <div className="flex items-center justify-between mb-3">
                    <p className="text-xs text-slate-500 dark:text-slate-400">
                      从数据到决策的完整链路，共 {transparencyData.decisions.length} 个步骤
                    </p>
                    <button
                      onClick={loadTransparencyData}
                      className="text-xs text-purple-600 hover:text-purple-700 flex items-center gap-1"
                    >
                      <RefreshCw className="w-3 h-3" />
                      刷新
                    </button>
                  </div>
                  <div className="relative">
                    <div className="absolute left-4 top-0 bottom-0 w-0.5 bg-slate-200 dark:bg-slate-700" />
                    {transparencyData.decisions.map((dec, idx) => (
                      <div key={idx} className="relative pl-12 pb-4">
                        <div className="absolute left-2 top-1 w-5 h-5 rounded-full bg-purple-500 text-white text-xs flex items-center justify-center font-bold">
                          {dec.step}
                        </div>
                        <div className="border border-slate-200 dark:border-slate-700 rounded-lg p-3">
                          <div className="flex items-center gap-2 mb-1">
                            <span className="text-xs px-2 py-0.5 rounded bg-brand-100 text-brand-700 dark:bg-brand-900/30 dark:text-brand-300 font-medium">
                              {dec.agent}
                            </span>
                            <span className="text-sm font-semibold text-slate-800 dark:text-slate-100">{dec.action}</span>
                          </div>
                          <p className="text-xs text-slate-500 dark:text-slate-400 mb-2">{dec.reason}</p>
                          <div className="flex flex-wrap gap-1">
                            {dec.data_used.map((d, i) => (
                              <span key={i} className="text-xs px-1.5 py-0.5 rounded bg-slate-100 dark:bg-slate-700 text-slate-500 dark:text-slate-400 font-mono">
                                {d.length > 30 ? d.substring(0, 30) + '...' : d}
                              </span>
                            ))}
                          </div>
                          <p className="text-xs text-slate-400 mt-2">{dec.timestamp}</p>
                        </div>
                      </div>
                    ))}
                  </div>
                </>
              )}
            </div>
          )}
        </div>
      )}

      <div className="card p-3 flex items-center gap-3">
        <div className="flex items-center gap-2 text-xs text-slate-500 dark:text-slate-400">
          <Filter className="w-3.5 h-3.5" />
          <span>筛选：</span>
        </div>
        <div className="flex items-center gap-1">
          {filterOptions.map((opt) => {
            const isActive = activeFilter === opt.key
            return (
              <button
                key={opt.key}
                onClick={() => setActiveFilter(opt.key)}
                className={`px-3 py-1.5 rounded-lg text-xs font-medium transition-colors ${
                  isActive
                    ? 'bg-brand-50 text-brand-700 dark:bg-brand-900/30 dark:text-brand-300'
                    : 'text-slate-600 hover:bg-slate-100 dark:text-slate-400 dark:hover:bg-slate-700'
                }`}
              >
                {opt.label}
              </button>
            )
          })}
        </div>

        <div className="ml-auto flex items-center gap-3 text-xs text-slate-400">
          {activitiesSource === 'real' ? (
            <span className="inline-flex items-center gap-1 px-2 py-0.5 rounded bg-green-100 text-green-700 dark:bg-green-900/30 dark:text-green-400">
              <span className="w-1.5 h-1.5 rounded-full bg-green-500"></span>
              实时数据
            </span>
          ) : activitiesSource === 'error' ? (
            <span className="inline-flex items-center gap-1 px-2 py-0.5 rounded bg-red-100 text-red-700 dark:bg-red-900/30 dark:text-red-400">
              <span className="w-1.5 h-1.5 rounded-full bg-red-500"></span>
              数据加载失败
            </span>
          ) : (
            <span className="inline-flex items-center gap-1 px-2 py-0.5 rounded bg-yellow-100 text-yellow-700 dark:bg-yellow-900/30 dark:text-yellow-400">
              <span className="w-1.5 h-1.5 rounded-full bg-yellow-500 animate-pulse"></span>
              加载中...
            </span>
          )}
          <span className="flex items-center gap-1">共 {filteredActivities.length} 条记录</span>
          <span>·</span>
          <span>最近刷新：{lastRefresh.toLocaleTimeString()}</span>
        </div>
      </div>

      <div ref={feedRef} className="card p-0 overflow-hidden">
        <div className="h-[320px] overflow-y-auto custom-scrollbar">
          <div className="divide-y divide-slate-100 dark:divide-slate-700">
            {filteredActivities.map((activity, idx) => {
              const typeConfig = activityTypeConfig[activity.type]
              const isLatest = idx === 0
              return (
                <div
                  key={activity.id}
                  className={`flex items-start gap-3 px-4 py-2 transition-colors ${
                    activity.subDescription ? 'min-h-[64px]' : 'h-[56px]'
                  } ${
                    isLatest
                      ? 'bg-brand-50/50 dark:bg-brand-900/10'
                      : 'hover:bg-slate-50 dark:hover:bg-slate-800/50'
                  }`}
                >
                  <div className="flex items-center gap-1.5 w-24 shrink-0 pt-0.5">
                    <Clock className="w-3 h-3 text-slate-400" />
                    <span className="text-xs font-mono text-slate-500 dark:text-slate-400">
                      {activity.time}
                    </span>
                  </div>

                  <div className="w-8 h-8 rounded-lg bg-slate-100 dark:bg-slate-700 flex items-center justify-center shrink-0">
                    {roleIconMap[activity.agentRole]}
                  </div>

                  <div className="flex-1 min-w-0 overflow-hidden">
                    <div className="flex items-center gap-2 mb-0.5">
                      <span className="text-xs font-semibold text-slate-700 dark:text-slate-300">
                        {AGENT_ROLE_NAMES[activity.agentRole]}
                      </span>
                      <span className="text-slate-300 dark:text-slate-600">·</span>
                      <span className={`inline-flex items-center gap-1 text-xs ${typeConfig.color}`}>
                        {typeConfig.icon}
                        {typeConfig.label}
                      </span>
                      {isLatest && (
                        <span className="badge bg-brand-100 text-brand-700 dark:bg-brand-900/30 dark:text-brand-300 ml-1">
                          NEW
                        </span>
                      )}
                    </div>
                    <p className="text-sm text-slate-700 dark:text-slate-300 leading-snug">
                      {activity.description}
                    </p>
                    {activity.subDescription ? (
                      <p className="mt-1 text-xs text-slate-500 dark:text-slate-400 leading-snug line-clamp-2">
                        {activity.subDescription}
                      </p>
                    ) : null}
                  </div>

                  <div className="pt-2.5 shrink-0">
                    <span
                      className={`w-2 h-2 rounded-full ${typeConfig.dot} ${isLatest ? 'animate-pulse' : ''}`}
                    />
                  </div>
                </div>
              )
            })}
          </div>

          {filteredActivities.length === 0 && (
            <div className="flex flex-col items-center justify-center py-16 text-slate-400 dark:text-slate-500">
              {activitiesSource === 'error' ? (
                <>
                  <AlertTriangle className="w-10 h-10 mb-3 text-red-500 opacity-50" />
                  <p className="text-sm text-red-500">数据加载失败</p>
                  <p className="text-xs mt-2">请检查后端服务连接状态，然后点击刷新按钮重试</p>
                  <button
                    onClick={loadRealActivities}
                    className="mt-3 text-xs text-brand-500 hover:text-brand-600 dark:hover:text-brand-400 inline-flex items-center gap-1"
                  >
                    <RefreshCw className="w-3 h-3" />
                    点击重试
                  </button>
                </>
              ) : activitiesSource === 'loading' ? (
                <>
                  <RefreshCw className="w-10 h-10 mb-3 text-yellow-500 opacity-50 animate-spin" />
                  <p className="text-sm">正在加载数据...</p>
                </>
              ) : (
                <>
                  <ActivityIcon className="w-10 h-10 mb-3 opacity-50" />
                  <p className="text-sm">当前筛选条件下暂无活动记录</p>
                  <button
                    onClick={() => setActiveFilter('all')}
                    className="mt-3 text-xs text-brand-500 hover:text-brand-600 dark:hover:text-brand-400"
                  >
                    清除筛选
                  </button>
                </>
              )}
            </div>
          )}
        </div>

        <div className="flex items-center justify-between px-4 py-2.5 bg-slate-50 dark:bg-slate-900/50 border-t border-slate-200 dark:border-slate-700">
          <div className="flex items-center gap-4 text-xs text-slate-500 dark:text-slate-400">
            <span className="flex items-center gap-1.5">
              <span className="w-1.5 h-1.5 rounded-full bg-green-500 animate-pulse" />
              {isAutoRefresh ? '实时监控中' : '已暂停'}
            </span>
            <span className="text-slate-300 dark:text-slate-600">|</span>
            <span>Agent 在线：CIO · Planner · Quant · Risk · Trader</span>
          </div>
          <div className="flex items-center gap-3 text-xs text-slate-400">
            <span className="flex items-center gap-1">
              <Cpu className="w-3 h-3 text-cyan-500" />
              研究分析
            </span>
            <span className="flex items-center gap-1">
              <Shield className="w-3 h-3 text-brand-500" />
              决策
            </span>
            <span className="flex items-center gap-1">
              <AlertTriangle className="w-3 h-3 text-yellow-500" />
              风控
            </span>
            <span className="flex items-center gap-1">
              <Zap className="w-3 h-3 text-green-500" />
              执行
            </span>
          </div>
        </div>
      </div>

      {/* 5 个 Agent 工作明细 · Agent 状态面板 */}
      <div className="card p-5">
        <div className="flex items-center justify-between mb-4">
          <h2 className="text-sm font-semibold text-slate-700 dark:text-slate-300">
            5 个 Agent 工作明细 · {phaseLabels[phase]}
          </h2>
          <div className="flex items-center gap-2">
            <span className={`text-xs px-2 py-0.5 rounded ${agentWorkDetailsSource === 'loading' ? 'bg-yellow-100 text-yellow-700 dark:bg-yellow-900/30 dark:text-yellow-400' : agentWorkDetailsSource === 'real' ? 'bg-green-100 text-green-700 dark:bg-green-900/30 dark:text-green-400' : 'bg-red-100 text-red-700 dark:bg-red-900/30 dark:text-red-400'}`}>
              {agentWorkDetailsSource === 'loading' ? '加载中' : agentWorkDetailsSource === 'real' ? '真实数据' : '数据异常'}
            </span>
            {agentWorkDetailsLoading && (
              <RefreshCw className="w-3 h-3 animate-spin text-slate-400" />
            )}
            <div className="flex items-center gap-1">
              {(Object.keys(agentWorkDetails) as AgentRole[]).map((role) => (
                <button
                  key={role}
                  onClick={() => setSelectedAgent(role)}
                  className={`px-2.5 py-1 text-xs rounded-md transition-colors ${
                    selectedAgent === role
                      ? 'bg-brand-100 text-brand-700 dark:bg-brand-900/30 dark:text-brand-300'
                      : 'text-slate-500 hover:bg-slate-100 dark:hover:bg-slate-700'
                  }`}
                >
                  {AGENT_ROLE_NAMES[role]}
                </button>
              ))}
            </div>
          </div>
        </div>

        <div className="grid grid-cols-5 gap-3">
          {(Object.keys(agentWorkDetails) as AgentRole[]).map((role) => {
            const d = agentWorkDetails[role]
            const isSelected = selectedAgent === role
            const stateColor = d.state === '准备中' ? 'text-sky-500' : d.state === '工作中' || d.state === '执行中' ? 'text-blue-500' : d.state === '监控中' ? 'text-green-500' : d.state === '休息中' ? 'text-yellow-500' : d.state === '已阻断' ? 'text-amber-500' : d.state === '已完成' ? 'text-emerald-500' : d.state === '错误' || d.state === '加载中' || d.state === '异常' ? 'text-red-500' : 'text-slate-400'
            return (
              <div
                key={role}
                onClick={() => setSelectedAgent(role)}
                className={`p-3 rounded-lg cursor-pointer transition-all ${
                  isSelected
                    ? 'border-2 border-brand-400 bg-brand-50 dark:bg-brand-900/20'
                    : 'border border-slate-200 dark:border-slate-700 hover:border-slate-300'
                }`}
              >
                <div className="flex items-center gap-2 mb-2">
                  {roleIconMap[role]}
                  <span className="text-xs font-semibold text-slate-700 dark:text-slate-300">
                    {AGENT_ROLE_NAMES[role]}
                  </span>
                </div>
                <div className={`text-xs mb-2 ${stateColor}`}>
                  {d.state}
                </div>
                <div className="text-xs text-slate-600 dark:text-slate-400 line-clamp-2 mb-2">
                  {d.currentTask}
                </div>
                <div className="h-1.5 bg-slate-200 dark:bg-slate-700 rounded-full overflow-hidden">
                  <div
                    className={`h-full ${d.progress >= 100 || d.state === '已完成' ? 'bg-gradient-to-r from-emerald-500 to-green-400' : 'bg-gradient-to-r from-brand-500 to-cyan-400'}`}
                    style={{ width: `${Math.min(d.progress, 100)}%` }}
                  />
                </div>
                <div className={`text-xs mt-1 text-right ${d.progress >= 100 || d.state === '已完成' ? 'text-emerald-500' : 'text-slate-400'}`}>
                  {d.state === '已完成' ? '✓ 已完成' : `${d.progress}%`}
                </div>
              </div>
            )
          })}
        </div>

        <div className="mt-4 p-4 rounded-lg bg-slate-50 dark:bg-slate-900/50">
          <div className="flex items-center gap-2 mb-3">
            {roleIconMap[selectedAgent]}
            <span className="text-sm font-semibold text-slate-700 dark:text-slate-300">
              {AGENT_ROLE_NAMES[selectedAgent]} · 今日任务日志
            </span>
          </div>
          <div className="space-y-2 h-[200px] overflow-y-auto pr-2">
            {agentWorkDetails[selectedAgent].todayTasks.map((task, idx) => (
              <div key={idx} className="flex items-start gap-2 text-xs">
                <span className="text-slate-400 mt-0.5">•</span>
                <span className="text-slate-600 dark:text-slate-400">{task}</span>
              </div>
            ))}
          </div>
        </div>
      </div>
    </div>
  )
}