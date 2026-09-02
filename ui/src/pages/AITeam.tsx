import { useState, useEffect, useCallback, useRef } from 'react'
import {
  Shield, Sparkles, Cpu, AlertTriangle, Zap, User,
  CheckCircle, XCircle, FileText, ChevronDown, ChevronRight,
  Circle, Clock, Play,
} from 'lucide-react'
import { useToastStore } from '../store/toastStore'
import {
  AgentRole, AgentState, AGENT_ROLE_NAMES, AGENT_ROLE_DESCRIPTIONS,
  AGENT_PIPELINE, AGENT_STATE_LABELS,
} from '../types'
import { GetAllAgentTasks } from '../../wailsjs/go/main/App'
import { getCurrentPhase, phaseLabels, MarketPhase } from '../utils/marketPhase'

interface TeamMemberData {
  role: AgentRole
  name: string
  state: AgentState
  currentAction: string
}

// 根据时段和角色获取状态
const getAgentStateByPhase = (role: AgentRole, phase: MarketPhase, taskStatus?: string): { state: AgentState; action: string } => {
  // 如果有任务状态数据，优先使用
  if (taskStatus === 'EXECUTING') {
    return { state: 'WORKING' as AgentState, action: '执行任务中...' }
  }
  if (taskStatus === 'HAS_ERRORS') {
    return { state: 'FAILED' as AgentState, action: '任务执行失败' }
  }

  switch (phase) {
    case 'PRE_MARKET':
      // 交易日盘前准备，等待开盘
      return { state: 'WORKING' as AgentState, action: '盘前准备，等待开盘' }
    case 'PRE_OPEN':
      // 盘前时段，所有角色准备工作
      return { state: 'WORKING' as AgentState, action: '盘前准备中' }
    case 'MORNING_SESSION':
    case 'AFTERNOON_SESSION':
      // 交易时段
      switch (role) {
        case 'TRADER':
          return { state: 'WORKING' as AgentState, action: '执行交易中' }
        case 'RISK':
          return { state: 'WORKING' as AgentState, action: '盘中风险监控' }
        case 'CIO':
          return { state: 'WORKING' as AgentState, action: '决策评估中' }
        case 'QUANT':
          return { state: 'IDLE' as AgentState, action: '等待交易信号' }
        case 'PLANNER':
          return { state: 'IDLE' as AgentState, action: '监控市场变化' }
        default:
          return { state: 'WORKING' as AgentState, action: '工作中' }
      }
    case 'LUNCH':
      // 午休时段
      return { state: 'RESTING' as AgentState, action: '午休中' }
    case 'POST_MARKET':
      // 盘后时段
      return { state: 'WORKING' as AgentState, action: '盘后确认交易中' }
    case 'REVIEW':
      // 复盘时段 (15:15+)
      const hour = new Date().getHours()
      if (hour >= 17 || hour < 16) {
        // 17:00之后或16:00前，显示已完成
        if (role === 'CIO') {
          return { state: 'COMPLETED' as AgentState, action: '今日投资报告已生成' }
        }
        return { state: 'COMPLETED' as AgentState, action: '今日任务已完成' }
      }
      // 16:00-17:00之间，显示执行中
      return { state: 'WORKING' as AgentState, action: '执行盘后任务中' }
    case 'CLOSED':
    default:
      // 休市
      return { state: 'COMPLETED' as AgentState, action: '休市中，等待下一交易日' }
  }
}

const teamMembers: TeamMemberData[] = [
  { role: 'CIO', name: '首席投资官 Agent', state: 'THINKING', currentAction: '综合投资规划、量化研究和风控报告，形成今日投资决策' },
  { role: 'PLANNER', name: '投资规划师 Agent', state: 'PROPOSING', currentAction: '确认用户 Investment Mandate 是否有变化' },
  { role: 'QUANT', name: '量化分析师 Agent', state: 'TOOL_CALLING', currentAction: '运行多因子模型，挖掘 Alpha 信号，生成候选池' },
  { role: 'RISK', name: '风控师 Agent', state: 'REVIEWING', currentAction: '独立审查组合风险，准备行使否决权' },
  { role: 'TRADER', name: '操盘手 Agent', state: 'WAITING', currentAction: '等待 CIO 最终决策 + 风控通过，准备执行交易' },
]

const roleIconMap: Record<AgentRole, React.ReactNode> = {
  CIO: <Shield className="w-5 h-5 text-brand-500" />,
  PLANNER: <Sparkles className="w-5 h-5 text-purple-500" />,
  QUANT: <Cpu className="w-5 h-5 text-cyan-500" />,
  RISK: <AlertTriangle className="w-5 h-5 text-yellow-500" />,
  TRADER: <Zap className="w-5 h-5 text-green-500" />,
}

const stateGroup = (state: AgentState): 'normal' | 'working' | 'error' => {
  if (state === 'FAILED') return 'error'
  if (['IDLE', 'COMPLETED', 'APPROVED', 'FILLED', 'VERIFIED'].includes(state)) return 'normal'
  return 'working'
}

const stateBadgeStyles: Record<string, string> = {
  normal: 'bg-green-100 text-green-700 dark:bg-green-900/30 dark:text-green-400 border border-green-200 dark:border-green-800',
  working: 'bg-yellow-100 text-yellow-700 dark:bg-yellow-900/30 dark:text-yellow-400 border border-yellow-200 dark:border-yellow-800',
  error: 'bg-red-100 text-red-700 dark:bg-red-900/30 dark:text-red-400 border border-red-200 dark:border-red-800',
}

const stateDotStyles: Record<string, string> = {
  normal: 'bg-green-500',
  working: 'bg-yellow-500',
  error: 'bg-red-500',
}

const statusLabelMap: Record<string, string> = { normal: '正常', working: '工作中', error: '异常' }

// ============ 任务规格清单（与后台 getOptimizedTasks 一一对应，共16任务） ============
const agentTasks: Record<AgentRole, { id: string; name: string; phase: string }[]> = {
  PLANNER: [
    { id: 'PLN-001', name: '盘前市场分析', phase: '盘前' },
  ],
  QUANT: [
    { id: 'QNT-001', name: '盘前量化分析', phase: '盘前' },
    { id: 'QNT-002', name: '信号监控', phase: '盘中' },
    { id: 'QNT-003', name: '策略参数调优', phase: '盘后' },
    { id: 'QNT-004', name: '因子复盘', phase: '复盘' },
  ],
  CIO: [
    { id: 'CIO-001', name: '生成盘前决策', phase: '盘前' },
    { id: 'CIO-002', name: '事件决策', phase: '盘中' },
    { id: 'CIO-003', name: '日终投资报告', phase: '盘后' },
    { id: 'CIO-004', name: '深度复盘与制定明日计划', phase: '复盘' },
  ],
  RISK: [
    { id: 'RSK-001', name: '盘前风险检查', phase: '盘前' },
    { id: 'RSK-002', name: '风险监控', phase: '盘中' },
    { id: 'RSK-003', name: '事件风险评估', phase: '盘中' },
    { id: 'RSK-004', name: '日终风险审查', phase: '盘后' },
  ],
  TRADER: [
    { id: 'TRD-001', name: '市场监控', phase: '盘中' },
    { id: 'TRD-002', name: '事件交易执行', phase: '盘中' },
    { id: 'TRD-003', name: '日终结算', phase: '盘后' },
  ],
}

// ============ 权限定义 ============

export default function AITeam() {
  const [expandedRole, setExpandedRole] = useState<AgentRole | null>(null)
  const [agentTaskStatus, setAgentTaskStatus] = useState<Record<string, { status: string; count: number }>>({})
  const [currentPhase, setCurrentPhase] = useState<MarketPhase>(getCurrentPhase())
  const [memberStates, setMemberStates] = useState<Record<string, { state: AgentState; action: string }>>({})

  // 根据当前时段和任务状态更新智能体状态
  const updateStates = useCallback(() => {
    const phase = getCurrentPhase()
    setCurrentPhase(phase)

    const newStates: Record<string, { state: AgentState; action: string }> = {}
    for (const member of teamMembers) {
      const taskStatus = agentTaskStatus[member.role]?.status
      const { state, action } = getAgentStateByPhase(member.role, phase, taskStatus)
      newStates[member.role] = { state, action }
    }
    setMemberStates(newStates)
  }, [agentTaskStatus])

  useEffect(() => {
    updateStates()
    // 每分钟更新一次状态
    const interval = setInterval(updateStates, 60000)
    return () => clearInterval(interval)
  }, [updateStates])

  const taskNotifiedRef = useRef(false)
  useEffect(() => {
    const loadAgentTasks = async () => {
      try {
        const result = await GetAllAgentTasks() as Record<string, { id: string; status: string }[]>
        const statusMap: Record<string, { status: string; count: number }> = {}
        for (const [role, tasks] of Object.entries(result || {})) {
          const list = tasks as { status: string }[]
          if (list && list.length > 0) {
            const executing = list.filter((t) => t.status === 'EXECUTING' || t.status === 'IN_PROGRESS').length
            const completed = list.filter((t) => t.status === 'COMPLETED' || t.status === 'APPROVED').length
            const failed = list.filter((t) => t.status === 'FAILED').length
            const status = failed > 0 ? 'HAS_ERRORS' : executing > 0 ? 'EXECUTING' : completed > 0 ? 'COMPLETED' : 'PENDING'
            statusMap[role.toUpperCase()] = { status, count: list.length }
          }
        }
        setAgentTaskStatus(statusMap)
        taskNotifiedRef.current = false
      } catch (err) {
        // 数据获取失败：弹窗警示（已每30秒轮询自动重试，首次失败弹一次）
        if (!taskNotifiedRef.current) {
          taskNotifiedRef.current = true
          useToastStore.getState().error(
            '智能体任务数据获取失败，将自动重试',
            err instanceof Error ? err.message : String(err)
          )
        }
      }
    }
    loadAgentTasks()
    const interval = setInterval(loadAgentTasks, 30000)
    return () => clearInterval(interval)
  }, [])

  const toggleRole = (role: AgentRole) => {
    setExpandedRole(expandedRole === role ? null : role)
  }

  const getTaskStatusBadge = (status: string) => {
    switch (status) {
      case 'EXECUTING': return { label: '执行中', class: 'bg-blue-100 text-blue-700 dark:bg-blue-900/30 dark:text-blue-400' }
      case 'COMPLETED': return { label: '已完成', class: 'bg-green-100 text-green-700 dark:bg-green-900/30 dark:text-green-400' }
      case 'HAS_ERRORS': return { label: '有错误', class: 'bg-red-100 text-red-700 dark:bg-red-900/30 dark:text-red-400' }
      default: return { label: '等待中', class: 'bg-slate-100 text-slate-600 dark:bg-slate-700 dark:text-slate-400' }
    }
  }

  return (
    <div className="space-y-5">
      {/* Header */}
      <header className="flex items-center justify-between">
        <div className="flex items-center gap-3">
          <div className="w-10 h-10 rounded-lg bg-gradient-to-br from-brand-500 to-cyan-400 flex items-center justify-center shadow-sm">
            <User className="w-5 h-5 text-white" />
          </div>
          <div>
            <h1 className="text-lg font-bold text-slate-800 dark:text-slate-100">AI 投资团队</h1>
            <p className="text-xs text-slate-500 dark:text-slate-400">
              五智能体架构 · PROPOSE → EVIDENCE → VETO → DECIDE → EXECUTE
            </p>
          </div>
        </div>
        <div className="flex items-center gap-2">
          <span className="badge bg-green-100 text-green-700 dark:bg-green-900/30 dark:text-green-400">
            <span className="w-1.5 h-1.5 rounded-full bg-green-500 animate-pulse mr-1" />
            5 个 Agent 在线
          </span>
        </div>
      </header>

      {/* 当前市场时段指示 */}
      <div className="mb-4 p-3 rounded-lg bg-slate-100 dark:bg-slate-800 flex items-center justify-between">
        <div className="flex items-center gap-2">
          <Clock className="w-4 h-4 text-slate-500" />
          <span className="text-sm text-slate-700 dark:text-slate-300">
            当前时段: <span className="font-semibold">{phaseLabels[currentPhase]}</span>
          </span>
        </div>
        <span className="text-xs text-slate-500">
          {new Date().toLocaleTimeString('zh-CN')}
        </span>
      </div>

      {/* Team Member Cards with Task Status */}
      <div className="grid grid-cols-5 gap-4">
        {teamMembers.map((member) => {
          const dynamicState = memberStates[member.role]
          const displayState = dynamicState?.state || member.state
          const displayAction = dynamicState?.action || member.currentAction
          const group = stateGroup(displayState)
          const taskStatus = agentTaskStatus[member.role]
          return (
            <div key={member.role} className="card p-4 flex flex-col">
              <div className="flex items-center justify-between mb-3">
                <div className="flex items-center gap-2">
                  <div className="w-9 h-9 rounded-lg bg-slate-100 dark:bg-slate-700 flex items-center justify-center">
                    {roleIconMap[member.role]}
                  </div>
                </div>
                <span className={`badge ${stateBadgeStyles[group]}`}>
                  <span className={`w-1.5 h-1.5 rounded-full mr-1 ${stateDotStyles[group]} ${group === 'working' ? 'animate-pulse' : ''}`} />
                  {statusLabelMap[group]}
                </span>
              </div>
              <div className="text-xs text-slate-500 dark:text-slate-400 mb-1">{AGENT_ROLE_NAMES[member.role]}</div>
              <div className="text-sm font-semibold text-slate-800 dark:text-slate-100 mb-2">{member.name}</div>
              <div className="flex items-center gap-1.5 text-xs text-slate-500 dark:text-slate-400 mb-2">
                <span className="font-mono bg-slate-100 dark:bg-slate-700 px-1.5 py-0.5 rounded">{AGENT_STATE_LABELS[displayState]}</span>
              </div>
              <p className="text-xs text-slate-600 dark:text-slate-400 leading-relaxed flex-1 mb-2">
                {displayAction}
              </p>
              {taskStatus && (
                <div className="border-t border-slate-200 dark:border-slate-700 pt-2">
                  <div className="flex items-center justify-between">
                    <span className="text-[10px] text-slate-500 dark:text-slate-400">任务状态</span>
                    <span className={`text-[10px] px-1.5 py-0.5 rounded ${getTaskStatusBadge(taskStatus.status).class}`}>
                      {getTaskStatusBadge(taskStatus.status).label} · {taskStatus.count}项
                    </span>
                  </div>
                </div>
              )}
            </div>
          )
        })}
      </div>

      {/* Task Specifications per Agent */}
      <div className="card p-5">
        <div className="flex items-center gap-2 mb-4">
          <FileText className="w-4 h-4 text-brand-500" />
          <h2 className="text-sm font-semibold text-slate-700 dark:text-slate-300">Agent 任务规格清单</h2>
          <span className="text-xs text-slate-400 dark:text-slate-500 ml-auto">
            点击展开查看每个 Agent 的任务 ID、名称和执行时段
          </span>
        </div>

        <div className="space-y-2">
          {(['PLANNER', 'QUANT', 'CIO', 'RISK', 'TRADER'] as AgentRole[]).map((role) => {
            const desc = AGENT_ROLE_DESCRIPTIONS[role]
            const tasks = agentTasks[role]
            const expanded = expandedRole === role
            return (
              <div key={role} className="border border-slate-200 dark:border-slate-700 rounded-lg overflow-hidden">
                <button
                  className="w-full flex items-center justify-between px-4 py-3 bg-slate-50 dark:bg-slate-900/50 hover:bg-slate-100 dark:hover:bg-slate-800/50 transition-colors"
                  onClick={() => toggleRole(role)}
                >
                  <div className="flex items-center gap-3">
                    <div className="w-8 h-8 rounded-lg bg-white dark:bg-slate-800 flex items-center justify-center border border-slate-200 dark:border-slate-700">
                      {roleIconMap[role]}
                    </div>
                    <div className="text-left">
                      <div className="text-sm font-semibold text-slate-800 dark:text-slate-100">{AGENT_ROLE_NAMES[role]} · {desc.title}</div>
                      <div className="text-xs text-slate-500 dark:text-slate-400">{desc.duty} · {tasks.length} 项任务</div>
                    </div>
                  </div>
                  <div className="flex items-center gap-3">
                    <div className="hidden md:flex gap-3 text-xs text-slate-500 dark:text-slate-400">
                      <span><CheckCircle className="inline w-3.5 h-3.5 text-green-500 mr-0.5" />可做 {desc.canDo.length}</span>
                      <span><XCircle className="inline w-3.5 h-3.5 text-red-400 mr-0.5" />不可做 {desc.cannotDo.length}</span>
                    </div>
                    {expanded ? <ChevronDown className="w-4 h-4 text-slate-400" /> : <ChevronRight className="w-4 h-4 text-slate-400" />}
                  </div>
                </button>
                {expanded && (
                  <div className="p-4 bg-white dark:bg-slate-800">
                    <div className="grid grid-cols-2 gap-4 mb-4">
                      <div>
                        <div className="text-xs font-medium text-green-700 dark:text-green-400 mb-1">✓ 可以做</div>
                        <ul className="space-y-1">
                          {desc.canDo.map((item, i) => (
                            <li key={i} className="text-xs text-slate-600 dark:text-slate-300 flex items-center gap-1">
                              <CheckCircle className="w-3 h-3 text-green-500" /> {item}
                            </li>
                          ))}
                        </ul>
                      </div>
                      <div>
                        <div className="text-xs font-medium text-red-700 dark:text-red-400 mb-1">✗ 不能做</div>
                        <ul className="space-y-1">
                          {desc.cannotDo.map((item, i) => (
                            <li key={i} className="text-xs text-slate-600 dark:text-slate-300 flex items-center gap-1">
                              <XCircle className="w-3 h-3 text-red-400" /> {item}
                            </li>
                          ))}
                        </ul>
                      </div>
                    </div>
                    <div className="border-t border-slate-200 dark:border-slate-700 pt-3">
                      <div className="text-xs font-medium text-slate-500 dark:text-slate-400 mb-2">📋 任务清单</div>
                      <div className="grid grid-cols-2 sm:grid-cols-3 lg:grid-cols-4 gap-2">
                        {tasks.map((task) => (
                          <div key={task.id} className="flex items-center gap-2 p-2 bg-slate-50 dark:bg-slate-900/50 rounded border border-slate-100 dark:border-slate-700">
                            <Circle className="w-3 h-3 text-brand-500 fill-brand-500" />
                            <div>
                              <div className="text-xs font-mono font-semibold text-brand-600 dark:text-brand-400">{task.id}</div>
                              <div className="text-xs text-slate-700 dark:text-slate-300">{task.name}</div>
                            </div>
                            <span className="ml-auto text-[10px] px-1.5 py-0.5 bg-slate-200 dark:bg-slate-700 text-slate-600 dark:text-slate-400 rounded">
                              {task.phase}
                            </span>
                          </div>
                        ))}
                      </div>
                    </div>
                  </div>
                )}
              </div>
            )
          })}
        </div>
      </div>

    </div>
  )
}
