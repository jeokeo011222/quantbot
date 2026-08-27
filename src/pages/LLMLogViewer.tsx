import { useState, useEffect, useRef } from 'react'
import {
  Brain,
  RefreshCw,
  Calendar,
  Clock,
  ChevronDown,
  ChevronUp,
  CheckCircle,
  XCircle,
  AlertTriangle,
  FileText,
} from 'lucide-react'
import { useToastStore } from '../store/toastStore'

interface LLMCallLogItem {
  id: number
  taskDate: string
  agentRole: string
  taskName: string
  phase: string
  model: string
  inputMessages: string
  outputContent: string
  finishReason: string
  toolCallCount: number
  promptTokens: number
  completionTokens: number
  totalTokens: number
  durationMs: number
  status: string
  errorMessage: string
  createdAt: string
}

const ROLE_COLORS: Record<string, string> = {
  CIO: 'bg-brand-100 text-brand-700 dark:bg-brand-900/30 dark:text-brand-400',
  PLANNER: 'bg-purple-100 text-purple-700 dark:bg-purple-900/30 dark:text-purple-400',
  QUANT: 'bg-cyan-100 text-cyan-700 dark:bg-cyan-900/30 dark:text-cyan-400',
  RISK: 'bg-yellow-100 text-yellow-700 dark:bg-yellow-900/30 dark:text-yellow-400',
  TRADER: 'bg-green-100 text-green-700 dark:bg-green-900/30 dark:text-green-400',
}

function renderInputMessages(inputMessages: string) {
  if (!inputMessages) return <div className="text-slate-400 text-xs">无输入记录</div>
  try {
    const parsed = JSON.parse(inputMessages)
    if (!Array.isArray(parsed) || parsed.length === 0) {
      return (
        <pre className="text-[11px] bg-slate-100 dark:bg-slate-900 p-3 rounded-lg overflow-auto max-h-96 whitespace-pre-wrap text-slate-700 dark:text-slate-300">
          {inputMessages}
        </pre>
      )
    }
    return (
      <div className="space-y-2">
        {parsed.map((msg: any, idx: number) => (
          <div key={idx} className="text-xs rounded-md border border-slate-200 dark:border-slate-700 overflow-hidden">
            <div className={`px-2 py-1 font-semibold text-[10px] uppercase ${
              msg.role === 'system'
                ? 'bg-amber-50 text-amber-700 dark:bg-amber-900/20 dark:text-amber-400'
                : msg.role === 'user'
                  ? 'bg-blue-50 text-blue-700 dark:bg-blue-900/20 dark:text-blue-400'
                  : 'bg-green-50 text-green-700 dark:bg-green-900/20 dark:text-green-400'
            }`}>
              {msg.role || 'message'}
              {msg.name ? ` · ${msg.name}` : ''}
            </div>
            <pre className="px-2 py-1.5 whitespace-pre-wrap overflow-auto max-h-72 text-slate-700 dark:text-slate-300">
              {typeof msg.content === 'string' ? msg.content : JSON.stringify(msg.content, null, 2)}
            </pre>
          </div>
        ))}
      </div>
    )
  } catch {
    return (
      <pre className="text-[11px] bg-slate-100 dark:bg-slate-900 p-3 rounded-lg overflow-auto max-h-96 whitespace-pre-wrap text-slate-700 dark:text-slate-300">
        {inputMessages}
      </pre>
    )
  }
}

export default function LLMLogViewer() {
  const [items, setItems] = useState<LLMCallLogItem[]>([])
  const [stats, setStats] = useState<{ call_count: number; total_tokens: number; date: string }>({
    call_count: 0,
    total_tokens: 0,
    date: '',
  })
  const [date, setDate] = useState('')
  const [role, setRole] = useState('')
  const [status, setStatus] = useState('')
  const [loading, setLoading] = useState(false)
  const [expandedId, setExpandedId] = useState<number | null>(null)
  const [enabled, setEnabled] = useState(false)

  const getApp = () => (window as any)['go']?.['main']?.['App']

  const fetchLogs = async () => {
    setLoading(true)
    try {
      const a = getApp()
      if (!a) return
      const logs = (await a.GetLLMCallLogs(date, role, status, 100)) as LLMCallLogItem[]
      if (logs) setItems(logs)
      const statsRes = (await a.GetLLMCallStats(date)) as { call_count: number; total_tokens: number; date: string }
      if (statsRes) setStats(statsRes)
      notifiedRef.current = false
    } catch (e) {
      console.error('Failed to fetch LLM logs:', e)
      setItems([])
      // 数据获取失败：弹窗警示 + 30 秒自动重试（首次失败弹一次）
      if (!notifiedRef.current) {
        notifiedRef.current = true
        useToastStore.getState().error(
          'LLM日志获取失败，将在30秒后自动重试',
          e instanceof Error ? e.message : String(e)
        )
      }
      if (retryTimerRef.current) clearTimeout(retryTimerRef.current)
      retryTimerRef.current = setTimeout(fetchLogs, 30000)
    } finally {
      setLoading(false)
    }
  }

  const retryTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null)
  const notifiedRef = useRef(false)

  useEffect(() => {
    const a = getApp()
    if (!a || typeof a.GetConfig !== 'function') return
    a.GetConfig()
      .then((cfg: any) => {
        if (cfg) setEnabled(!!cfg.enable_llm_logging)
      })
      .catch(() => {})
    fetchLogs()
    return () => {
      if (retryTimerRef.current) clearTimeout(retryTimerRef.current)
      notifiedRef.current = false
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  const toggleEnabled = async () => {
    const next = !enabled
    setEnabled(next)
    try {
      const a = getApp()
      if (a && typeof a.SetLLMLoggingEnabled === 'function') {
        await a.SetLLMLoggingEnabled(next)
      }
    } catch (e) {
      console.error('Failed to set LLM logging:', e)
      setEnabled(!next)
    }
  }

  return (
    <div className="space-y-5">
      {/* 参数开关 */}
      <div className="flex items-center justify-between rounded-lg border border-slate-200 dark:border-slate-700 px-3 py-2.5">
        <div className="flex-1 pr-3">
          <div className="text-sm font-medium text-slate-800 dark:text-slate-100">记录大模型输入/输出内容（审计）</div>
          <div className="text-xs text-slate-500 dark:text-slate-400 mt-0.5">
            开启后，每次大模型调用的完整输入提示词与输出内容将保存到数据库，可在下方查看。关闭后不再记录。
          </div>
        </div>
        <button
          type="button"
          onClick={toggleEnabled}
          className={`relative w-11 h-6 rounded-full transition-colors focus:outline-none ${enabled ? 'bg-blue-600' : 'bg-slate-300 dark:bg-slate-600'}`}
        >
          <span className={`absolute top-0.5 left-0.5 w-5 h-5 bg-white rounded-full shadow transition-transform ${enabled ? 'translate-x-5' : ''}`} />
        </button>
      </div>

      {/* Header */}
      <header className="flex items-center justify-between">
        <div className="flex items-center gap-3">
          <div className="w-10 h-10 rounded-lg bg-gradient-to-br from-indigo-500 to-purple-400 flex items-center justify-center shadow-sm">
            <Brain className="w-5 h-5 text-white" />
          </div>
          <div>
            <h1 className="text-lg font-bold text-slate-800 dark:text-slate-100">LLM 调用日志</h1>
            <p className="text-xs text-slate-500 dark:text-slate-400">
              记录大模型调用的完整输入提示词与输出内容，可按日期/角色/状态筛选
            </p>
          </div>
        </div>
        <button onClick={fetchLogs} className="btn-secondary text-xs py-1.5 px-3">
          <RefreshCw className={`w-3.5 h-3.5 mr-1 ${loading ? 'animate-spin' : ''}`} />
          刷新
        </button>
      </header>

      {/* Stats */}
      <div className="grid grid-cols-3 gap-3">
        <div className="card p-4">
          <div className="text-xs text-slate-500 dark:text-slate-400">调用次数{stats.date ? `（${stats.date}）` : ''}</div>
          <div className="text-2xl font-bold text-indigo-600 dark:text-indigo-400">{stats.call_count}</div>
        </div>
        <div className="card p-4">
          <div className="text-xs text-slate-500 dark:text-slate-400">Token 消耗{stats.date ? `（${stats.date}）` : ''}</div>
          <div className="text-2xl font-bold text-purple-600 dark:text-purple-400">{stats.total_tokens.toLocaleString()}</div>
        </div>
        <div className="card p-4">
          <div className="text-xs text-slate-500 dark:text-slate-400">记录条数</div>
          <div className="text-2xl font-bold text-slate-700 dark:text-slate-200">{items.length}</div>
        </div>
      </div>

      {/* Filters */}
      <div className="card p-3 flex flex-wrap items-center gap-3">
        <div className="flex items-center gap-2 text-xs text-slate-500 dark:text-slate-400">
          <Calendar className="w-3.5 h-3.5" />
          <span>筛选：</span>
        </div>

        <input
          type="date"
          value={date}
          onChange={(e) => setDate(e.target.value)}
          className="px-3 py-1.5 text-xs rounded-lg border border-slate-200 dark:border-slate-700 bg-white dark:bg-slate-900 dark:text-slate-300 focus:outline-none focus:ring-1 focus:ring-brand-400"
        />

        <select
          value={role}
          onChange={(e) => setRole(e.target.value)}
          className="px-3 py-1.5 text-xs rounded-lg border border-slate-200 dark:border-slate-700 bg-white dark:bg-slate-900 dark:text-slate-300 focus:outline-none focus:ring-1 focus:ring-brand-400"
        >
          <option value="">全部角色</option>
          <option value="CIO">CIO 首席投资官</option>
          <option value="PLANNER">Planner 市场研究</option>
          <option value="QUANT">Quant 量化分析</option>
          <option value="RISK">Risk 风险控制</option>
          <option value="TRADER">Trader 交易</option>
        </select>

        <select
          value={status}
          onChange={(e) => setStatus(e.target.value)}
          className="px-3 py-1.5 text-xs rounded-lg border border-slate-200 dark:border-slate-700 bg-white dark:bg-slate-900 dark:text-slate-300 focus:outline-none focus:ring-1 focus:ring-brand-400"
        >
          <option value="">全部状态</option>
          <option value="SUCCESS">成功</option>
          <option value="FAILED">失败</option>
        </select>

        <button onClick={fetchLogs} className="px-3 py-1.5 text-xs rounded-lg bg-indigo-600 text-white hover:bg-indigo-700 transition-colors">
          查询
        </button>

        <button
          onClick={() => { setDate(''); setRole(''); setStatus(''); fetchLogs() }}
          className="text-xs text-slate-500 hover:text-slate-700 dark:hover:text-slate-300"
        >
          清除
        </button>
      </div>

      {/* List */}
      <div className="card p-0 overflow-hidden">
        {loading ? (
          <div className="px-4 py-12 text-center text-slate-400 text-sm">加载中...</div>
        ) : items.length === 0 ? (
          <div className="px-4 py-12 text-center text-slate-400">
            <FileText className="w-10 h-10 mx-auto mb-2 opacity-50" />
            <p className="text-sm">暂无大模型调用记录</p>
            <p className="text-xs mt-1 opacity-70">请先开启上方«记录大模型输入/输出内容»开关</p>
          </div>
        ) : (
          <div className="divide-y divide-slate-100 dark:divide-slate-700">
            {items.map((item) => {
              const isExpanded = expandedId === item.id
              return (
                <div key={item.id}>
                  <button
                    onClick={() => setExpandedId(isExpanded ? null : item.id)}
                    className="w-full text-left px-4 py-3 flex items-center gap-3 hover:bg-slate-50 dark:hover:bg-slate-800/30 transition-colors"
                  >
                    <div className="flex-1 min-w-0">
                      <div className="flex items-center gap-2">
                        {item.status === 'SUCCESS' ? (
                          <CheckCircle className="w-3.5 h-3.5 text-green-500 shrink-0" />
                        ) : (
                          <XCircle className="w-3.5 h-3.5 text-red-500 shrink-0" />
                        )}
                        {item.agentRole && (
                          <span className={`inline-flex items-center px-2 py-0.5 rounded text-[10px] font-semibold shrink-0 ${ROLE_COLORS[item.agentRole] || 'bg-slate-100 text-slate-600'}`}>
                            {item.agentRole}
                          </span>
                        )}
                        <span className="text-xs font-medium text-slate-700 dark:text-slate-200 truncate">{item.taskName || '-'}</span>
                      </div>
                      <div className="flex flex-wrap items-center gap-x-3 gap-y-0.5 mt-1 text-[11px] text-slate-400">
                        <span className="inline-flex items-center gap-1">
                          <Clock className="w-3 h-3" />
                          {new Date(item.createdAt).toLocaleString('zh-CN', { hour12: false })}
                        </span>
                        {item.phase && (
                          <span className={`inline-flex items-center px-1.5 py-0.5 rounded text-[10px] font-medium ${
                            item.phase === 'IN_MARKET' ? 'bg-green-50 text-green-600 dark:bg-green-900/20 dark:text-green-400'
                            : item.phase === 'PRE_MARKET' ? 'bg-amber-50 text-amber-600 dark:bg-amber-900/20 dark:text-amber-400'
                            : item.phase === 'POST_MARKET' ? 'bg-blue-50 text-blue-600 dark:bg-blue-900/20 dark:text-blue-400'
                            : item.phase === 'REVIEW' ? 'bg-purple-50 text-purple-600 dark:bg-purple-900/20 dark:text-purple-400'
                            : 'bg-slate-100 text-slate-500 dark:bg-slate-800 dark:text-slate-400'
                          }`}>
                            {item.phase === 'PRE_MARKET' ? '盘前'
                              : item.phase === 'IN_MARKET' ? '盘中'
                              : item.phase === 'POST_MARKET' ? '盘后'
                              : item.phase === 'REVIEW' ? '复盘'
                              : item.phase}
                          </span>
                        )}
                        {item.model && <span>{item.model}</span>}
                        <span>token: {item.totalTokens}</span>
                        <span>耗时: {(item.durationMs / 1000).toFixed(2)}s</span>
                        {item.toolCallCount > 0 && <span>工具调用: {item.toolCallCount}</span>}
                      </div>
                    </div>
                    {isExpanded ? (
                      <ChevronUp className="w-4 h-4 text-slate-400 shrink-0" />
                    ) : (
                      <ChevronDown className="w-4 h-4 text-slate-400 shrink-0" />
                    )}
                  </button>

                  {isExpanded && (
                    <div className="px-4 pb-4 pt-1 bg-slate-50/50 dark:bg-slate-800/20">
                      <div className="grid grid-cols-2 md:grid-cols-4 gap-2 mb-3">
                        <div className="rounded-md bg-slate-100 dark:bg-slate-800 p-2">
                          <div className="text-[10px] text-slate-400">提示词Token</div>
                          <div className="text-sm font-semibold text-slate-700 dark:text-slate-200">{item.promptTokens}</div>
                        </div>
                        <div className="rounded-md bg-slate-100 dark:bg-slate-800 p-2">
                          <div className="text-[10px] text-slate-400">输出Token</div>
                          <div className="text-sm font-semibold text-slate-700 dark:text-slate-200">{item.completionTokens}</div>
                        </div>
                        <div className="rounded-md bg-slate-100 dark:bg-slate-800 p-2">
                          <div className="text-[10px] text-slate-400">结束原因</div>
                          <div className="text-sm font-semibold text-slate-700 dark:text-slate-200">{item.finishReason || '-'}</div>
                        </div>
                        <div className="rounded-md bg-slate-100 dark:bg-slate-800 p-2">
                          <div className="text-[10px] text-slate-400">阶段</div>
                          <div className="text-sm font-semibold text-slate-700 dark:text-slate-200">{item.phase || '-'}</div>
                        </div>
                      </div>

                      {item.status === 'FAILED' && item.errorMessage && (
                        <div className="flex items-start gap-2 mb-3 rounded-md bg-red-50 dark:bg-red-900/20 p-2 text-xs text-red-600 dark:text-red-400">
                          <AlertTriangle className="w-4 h-4 shrink-0 mt-0.5" />
                          <span>{item.errorMessage}</span>
                        </div>
                      )}

                      <div className="space-y-3">
                        <div>
                          <div className="text-xs font-semibold text-slate-600 dark:text-slate-300 mb-1">完整输入（messages）</div>
                          {renderInputMessages(item.inputMessages)}
                        </div>
                        <div>
                          <div className="text-xs font-semibold text-slate-600 dark:text-slate-300 mb-1">输出内容</div>
                          <pre className="text-[11px] bg-slate-100 dark:bg-slate-900 p-3 rounded-lg overflow-auto max-h-96 whitespace-pre-wrap text-slate-700 dark:text-slate-300">
                            {item.outputContent || '（无输出内容）'}
                          </pre>
                        </div>
                      </div>
                    </div>
                  )}
                </div>
              )
            })}
          </div>
        )}
      </div>
    </div>
  )
}