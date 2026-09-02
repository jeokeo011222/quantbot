import { useState, useEffect, useMemo, useRef } from 'react'
import {
  FileText,
  Search,
  Filter,
  Calendar,
  User,
  CheckCircle,
  XCircle,
  AlertCircle,
  RefreshCw,
  Download,
} from 'lucide-react'
import { useToastStore } from '../store/toastStore'

interface AuditLogItem {
  id: number
  eventId: string
  eventType: string
  userId: string
  userName: string
  action: string
  targetType: string
  targetId: string
  result: string
  detailsJson: string
  timestamp: string
}

interface AuditQueryResult {
  total: number
  page: number
  pageSize: number
  items: AuditLogItem[]
}

const EVENT_TYPE_LABELS: Record<string, string> = {
  LOGIN: '系统启动/登录',
  LOGOUT: '系统关闭/登出',
  SCREENING: '选股',
  STRATEGY: '策略',
  BACKTEST: '回测',
  AI_ANALYSIS: 'AI分析',
  PLANNER: '投资规划',
  LIVE_ACTIVITY: 'AI实时活动',
  CIO_DAILY_REVIEW: 'CIO每日复盘',
  APPROVAL: '审批',
  ORDER: '交易订单',
  CONFIG_CHANGE: '配置变更',
  TIER_CHANGE: '版本升级',
}

const eventTypeColors: Record<string, string> = {
  LOGIN: 'bg-blue-100 text-blue-700 dark:bg-blue-900/30 dark:text-blue-400',
  LOGOUT: 'bg-slate-100 text-slate-700 dark:bg-slate-700 dark:text-slate-400',
  SCREENING: 'bg-cyan-100 text-cyan-700 dark:bg-cyan-900/30 dark:text-cyan-400',
  STRATEGY: 'bg-purple-100 text-purple-700 dark:bg-purple-900/30 dark:text-purple-400',
  BACKTEST: 'bg-indigo-100 text-indigo-700 dark:bg-indigo-900/30 dark:text-indigo-400',
  AI_ANALYSIS: 'bg-emerald-100 text-emerald-700 dark:bg-emerald-900/30 dark:text-emerald-400',
  PLANNER: 'bg-pink-100 text-pink-700 dark:bg-pink-900/30 dark:text-pink-400',
  LIVE_ACTIVITY: 'bg-amber-100 text-amber-700 dark:bg-amber-900/30 dark:text-amber-400',
  CIO_DAILY_REVIEW: 'bg-orange-100 text-orange-700 dark:bg-orange-900/30 dark:text-orange-400',
  APPROVAL: 'bg-green-100 text-green-700 dark:bg-green-900/30 dark:text-green-400',
  ORDER: 'bg-red-100 text-red-700 dark:bg-red-900/30 dark:text-red-400',
  CONFIG_CHANGE: 'bg-slate-100 text-slate-700 dark:bg-slate-700 dark:text-slate-400',
  TIER_CHANGE: 'bg-yellow-100 text-yellow-700 dark:bg-yellow-900/30 dark:text-yellow-400',
}

const ROLE_COLORS: Record<string, string> = {
  CIO: 'bg-brand-100 text-brand-700 dark:bg-brand-900/30 dark:text-brand-400',
  PLANNER: 'bg-purple-100 text-purple-700 dark:bg-purple-900/30 dark:text-purple-400',
  QUANT: 'bg-cyan-100 text-cyan-700 dark:bg-cyan-900/30 dark:text-cyan-400',
  RISK: 'bg-yellow-100 text-yellow-700 dark:bg-yellow-900/30 dark:text-yellow-400',
  TRADER: 'bg-green-100 text-green-700 dark:bg-green-900/30 dark:text-green-400',
}

const ACTIVITY_TYPE_LABELS: Record<string, string> = {
  research: '研究分析',
  decision: '决策',
  risk_check: '风控检查',
  execution: '执行交易',
  alert: '警报',
}

const ACTIVITY_TYPE_ICONS: Record<string, string> = {
  research: '🔍',
  decision: '⚡',
  risk_check: '🛡️',
  execution: '📈',
  alert: '⚠️',
}

function parseLiveActivityDetails(detailsJson: string): { agentRole?: string; activityType?: string; description?: string; time?: string } | null {
  try {
    const parsed = JSON.parse(detailsJson)
    if (parsed && (parsed.agent_role || parsed.activity_type || parsed.description)) {
      return {
        agentRole: parsed.agent_role,
        activityType: parsed.activity_type,
        description: parsed.description,
        time: parsed.time,
      }
    }
    return null
  } catch {
    return null
  }
}

export default function AuditLogPage() {
  const [items, setItems] = useState<AuditLogItem[]>([])
  const [total, setTotal] = useState(0)
  const [page, setPage] = useState(1)
  const [pageSize] = useState(20)
  const [searchQuery, setSearchQuery] = useState('')
  const [eventTypeFilter, setEventTypeFilter] = useState('')
  const [startDate, setStartDate] = useState('')
  const [endDate, setEndDate] = useState('')
  const [loading, setLoading] = useState(false)

  const fetchAuditLogs = async () => {
    setLoading(true)
    try {
      const App: any = (window as any)['go']?.['main']?.['App']
      if (!App || typeof App.GetAuditLogs !== 'function') {
        setItems([])
        setTotal(0)
        return
      }
      const result = await App.GetAuditLogs(
        eventTypeFilter,
        '',
        startDate,
        endDate,
        page,
        pageSize
      ) as AuditQueryResult
      setItems(result.items || [])
      setTotal(result.total || 0)
    } catch (e) {
      console.error('Failed to fetch audit logs:', e)
      setItems([])
      setTotal(0)
      // 数据获取失败：弹窗警示 + 30 秒自动重试（首次失败弹一次）
      if (!notifiedRef.current) {
        notifiedRef.current = true
        useToastStore.getState().error(
          '审计日志获取失败，将在30秒后自动重试',
          e instanceof Error ? e.message : String(e)
        )
      }
      if (retryTimerRef.current) clearTimeout(retryTimerRef.current)
      retryTimerRef.current = setTimeout(fetchAuditLogs, 30000)
    } finally {
      setLoading(false)
    }
  }

  const retryTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null)
  const notifiedRef = useRef(false)

  useEffect(() => {
    fetchAuditLogs()
    return () => {
      if (retryTimerRef.current) clearTimeout(retryTimerRef.current)
      notifiedRef.current = false
    }
  }, [page, eventTypeFilter, startDate, endDate])

  const filteredBySearch = useMemo(() => {
    if (!searchQuery.trim()) return items
    const q = searchQuery.toLowerCase()
    return items.filter(
      (item) =>
        item.action.toLowerCase().includes(q) ||
        item.userName.toLowerCase().includes(q) ||
        item.targetId.toLowerCase().includes(q)
    )
  }, [items, searchQuery])

  const totalPages = Math.max(1, Math.ceil(total / pageSize))
  const stats = useMemo(() => {
    const byType: Record<string, number> = {}
    items.forEach((item) => {
      byType[item.eventType] = (byType[item.eventType] || 0) + 1
    })
    return byType
  }, [items])

  const handleExport = () => {
    const dataStr = JSON.stringify(items, null, 2)
    const blob = new Blob([dataStr], { type: 'application/json' })
    const url = URL.createObjectURL(blob)
    const a = document.createElement('a')
    a.href = url
    a.download = `audit_logs_${new Date().toISOString().split('T')[0]}.json`
    a.click()
    URL.revokeObjectURL(url)
  }

  return (
    <div className="space-y-5">
      <header className="flex items-center justify-between">
        <div className="flex items-center gap-3">
          <div className="w-10 h-10 rounded-lg bg-gradient-to-br from-brand-500 to-cyan-400 flex items-center justify-center shadow-sm">
            <FileText className="w-5 h-5 text-white" />
          </div>
          <div>
            <h1 className="text-lg font-bold text-slate-800 dark:text-slate-100">审计日志</h1>
            <p className="text-xs text-slate-500 dark:text-slate-400">系统所有操作的完整审计追踪</p>
          </div>
        </div>
        <div className="flex items-center gap-2">
          <button
            onClick={handleExport}
            className="btn-secondary text-xs py-1.5 px-3"
            disabled={items.length === 0}
          >
            <Download className="w-3.5 h-3.5 mr-1" />
            导出
          </button>
          <button
            onClick={fetchAuditLogs}
            className="btn-secondary text-xs py-1.5 px-3"
          >
            <RefreshCw className="w-3.5 h-3.5 mr-1" />
            刷新
          </button>
        </div>
      </header>

      {/* Stats */}
      <div className="grid grid-cols-4 gap-3">
        <div className="card p-4">
          <div className="text-xs text-slate-500 dark:text-slate-400">总记录数</div>
          <div className="text-2xl font-bold text-brand-600 dark:text-brand-400">{total}</div>
        </div>
        <div className="card p-4">
          <div className="text-xs text-slate-500 dark:text-slate-400">今日事件类型</div>
          <div className="text-2xl font-bold text-cyan-600 dark:text-cyan-400">{Object.keys(stats).length}</div>
        </div>
        <div className="card p-4">
          <div className="text-xs text-slate-500 dark:text-slate-400">成功操作</div>
          <div className="text-2xl font-bold text-green-600 dark:text-green-400">
            {items.filter((i) => i.result === 'success').length}
          </div>
        </div>
        <div className="card p-4">
          <div className="text-xs text-slate-500 dark:text-slate-400">失败/警告</div>
          <div className="text-2xl font-bold text-red-600 dark:text-red-400">
            {items.filter((i) => i.result !== 'success').length}
          </div>
        </div>
      </div>

      {/* Filters */}
      <div className="card p-3 flex flex-wrap items-center gap-3">
        <div className="flex items-center gap-2 text-xs text-slate-500 dark:text-slate-400">
          <Filter className="w-3.5 h-3.5" />
          <span>筛选：</span>
        </div>

        <div className="relative flex-1 min-w-[200px]">
          <Search className="absolute left-2.5 top-1/2 -translate-y-1/2 w-3.5 h-3.5 text-slate-400" />
          <input
            type="text"
            placeholder="搜索操作描述、用户、目标ID..."
            value={searchQuery}
            onChange={(e) => setSearchQuery(e.target.value)}
            className="w-full pl-8 pr-3 py-1.5 text-xs rounded-lg border border-slate-200 dark:border-slate-700 bg-white dark:bg-slate-900 dark:text-slate-300 focus:outline-none focus:ring-1 focus:ring-brand-400"
          />
        </div>

        <select
          value={eventTypeFilter}
          onChange={(e) => { setEventTypeFilter(e.target.value); setPage(1) }}
          className="px-3 py-1.5 text-xs rounded-lg border border-slate-200 dark:border-slate-700 bg-white dark:bg-slate-900 dark:text-slate-300 focus:outline-none focus:ring-1 focus:ring-brand-400"
        >
          <option value="">全部事件类型</option>
          {Object.entries(EVENT_TYPE_LABELS).map(([key, label]) => (
            <option key={key} value={key}>{label}</option>
          ))}
        </select>

        <div className="flex items-center gap-1">
          <Calendar className="w-3.5 h-3.5 text-slate-400" />
          <input
            type="date"
            value={startDate}
            onChange={(e) => { setStartDate(e.target.value); setPage(1) }}
            className="px-2 py-1 text-xs rounded-lg border border-slate-200 dark:border-slate-700 bg-white dark:bg-slate-900 dark:text-slate-300 focus:outline-none focus:ring-1 focus:ring-brand-400"
          />
          <span className="text-xs text-slate-400">至</span>
          <input
            type="date"
            value={endDate}
            onChange={(e) => { setEndDate(e.target.value); setPage(1) }}
            className="px-2 py-1 text-xs rounded-lg border border-slate-200 dark:border-slate-700 bg-white dark:bg-slate-900 dark:text-slate-300 focus:outline-none focus:ring-1 focus:ring-brand-400"
          />
        </div>

        <button
          onClick={() => {
            setSearchQuery('')
            setEventTypeFilter('')
            setStartDate('')
            setEndDate('')
            setPage(1)
          }}
          className="text-xs text-slate-500 hover:text-slate-700 dark:hover:text-slate-300"
        >
          清除筛选
        </button>
      </div>

      {/* Quick filter chips */}
      <div className="flex items-center gap-2 flex-wrap">
        <span className="text-xs text-slate-500 dark:text-slate-400">快捷筛选：</span>
        {Object.entries(EVENT_TYPE_LABELS)
          .filter(([key]) => ['LIVE_ACTIVITY', 'PLANNER', 'SCREENING', 'AI_ANALYSIS', 'APPROVAL', 'ORDER'].includes(key))
          .map(([key, label]) => (
            <button
              key={key}
              onClick={() => { setEventTypeFilter(eventTypeFilter === key ? '' : key); setPage(1) }}
              className={`px-2 py-0.5 rounded-full text-[11px] font-medium transition-colors ${
                eventTypeFilter === key
                  ? eventTypeColors[key] + ' ring-2 ring-offset-1 ring-offset-white dark:ring-offset-slate-800'
                  : 'bg-slate-100 text-slate-600 dark:bg-slate-800 dark:text-slate-400 hover:bg-slate-200 dark:hover:bg-slate-700'
              }`}
            >
              {label}
            </button>
          ))}
      </div>

      {/* Logs Table */}
      <div className="card p-0 overflow-hidden">
        <div className="overflow-x-auto">
          <table className="w-full text-sm">
            <thead className="bg-slate-50 dark:bg-slate-800/50">
              <tr>
                <th className="px-4 py-3 text-left text-xs font-semibold text-slate-500 dark:text-slate-400 uppercase">时间</th>
                <th className="px-4 py-3 text-left text-xs font-semibold text-slate-500 dark:text-slate-400 uppercase">事件类型</th>
                <th className="px-4 py-3 text-left text-xs font-semibold text-slate-500 dark:text-slate-400 uppercase">操作</th>
                <th className="px-4 py-3 text-left text-xs font-semibold text-slate-500 dark:text-slate-400 uppercase">目标</th>
                <th className="px-4 py-3 text-left text-xs font-semibold text-slate-500 dark:text-slate-400 uppercase">用户</th>
                <th className="px-4 py-3 text-left text-xs font-semibold text-slate-500 dark:text-slate-400 uppercase">结果</th>
                <th className="px-4 py-3 text-left text-xs font-semibold text-slate-500 dark:text-slate-400 uppercase">详情</th>
              </tr>
            </thead>
            <tbody className="divide-y divide-slate-100 dark:divide-slate-700">
              {loading ? (
                <tr>
                  <td colSpan={7} className="px-4 py-12 text-center text-slate-400">加载中...</td>
                </tr>
              ) : filteredBySearch.length === 0 ? (
                <tr>
                  <td colSpan={7} className="px-4 py-12 text-center text-slate-400">
                    <FileText className="w-10 h-10 mx-auto mb-2 opacity-50" />
                    <p>暂无审计记录</p>
                  </td>
                </tr>
              ) : (
                filteredBySearch.map((item) => {
                  const liveDetails = item.eventType === 'LIVE_ACTIVITY' ? parseLiveActivityDetails(item.detailsJson) : null
                  return (
                  <tr key={item.id} className={`hover:bg-slate-50 dark:hover:bg-slate-800/30 ${item.eventType === 'LIVE_ACTIVITY' ? 'border-l-2 border-l-amber-400' : ''}`}>
                    <td className="px-4 py-3 text-xs text-slate-600 dark:text-slate-400 whitespace-nowrap">
                      {new Date(item.timestamp).toLocaleString('zh-CN', {
                        year: 'numeric',
                        month: '2-digit',
                        day: '2-digit',
                        hour: '2-digit',
                        minute: '2-digit',
                        second: '2-digit',
                      })}
                    </td>
                    <td className="px-4 py-3">
                      <span className={`inline-flex items-center px-2 py-0.5 rounded text-xs font-medium ${eventTypeColors[item.eventType] || 'bg-slate-100 text-slate-700'}`}>
                        {EVENT_TYPE_LABELS[item.eventType] || item.eventType}
                      </span>
                    </td>
                    <td className="px-4 py-3 text-xs text-slate-700 dark:text-slate-300">
                      {liveDetails ? (
                        <div className="flex flex-col gap-1">
                          <div className="flex items-center gap-1.5">
                            {liveDetails.agentRole && (
                              <span className={`inline-flex items-center px-1.5 py-0.5 rounded text-[10px] font-semibold ${ROLE_COLORS[liveDetails.agentRole] || 'bg-slate-100 text-slate-600'}`}>
                                {liveDetails.agentRole}
                              </span>
                            )}
                            {liveDetails.activityType && (
                              <span className="text-[10px] text-slate-400">
                                {ACTIVITY_TYPE_ICONS[liveDetails.activityType] || '📌'} {ACTIVITY_TYPE_LABELS[liveDetails.activityType] || liveDetails.activityType}
                              </span>
                            )}
                          </div>
                          {liveDetails.description && (
                            <span className="text-xs text-slate-600 dark:text-slate-400">{liveDetails.description}</span>
                          )}
                        </div>
                      ) : (
                        item.action
                      )}
                    </td>
                    <td className="px-4 py-3 text-xs text-slate-500 dark:text-slate-400">
                      <span className="text-xs">{item.targetType}</span>
                      {item.targetId && <span className="ml-1 font-mono text-[10px]">{item.targetId}</span>}
                    </td>
                    <td className="px-4 py-3 text-xs text-slate-600 dark:text-slate-400">
                      <span className="inline-flex items-center gap-1">
                        <User className="w-3 h-3" />
                        {item.userName || item.userId || 'system'}
                      </span>
                    </td>
                    <td className="px-4 py-3">
                      {item.result === 'success' ? (
                        <CheckCircle className="w-4 h-4 text-green-500" />
                      ) : item.result === 'failed' ? (
                        <XCircle className="w-4 h-4 text-red-500" />
                      ) : (
                        <AlertCircle className="w-4 h-4 text-yellow-500" />
                      )}
                    </td>
                    <td className="px-4 py-3 text-xs text-slate-500 dark:text-slate-400 max-w-xs truncate">
                      {item.detailsJson ? (
                        <details>
                          <summary className="cursor-pointer text-brand-500 hover:text-brand-600">查看</summary>
                          <pre className="mt-1 text-[10px] bg-slate-100 dark:bg-slate-900 p-2 rounded overflow-auto max-h-32">
                            {item.detailsJson}
                          </pre>
                        </details>
                      ) : (
                        '-'
                      )}
                    </td>
                  </tr>
                )})
              )}
            </tbody>
          </table>
        </div>

        {/* Pagination */}
        {total > 0 && (
          <div className="flex items-center justify-between px-4 py-3 border-t border-slate-200 dark:border-slate-700">
            <div className="text-xs text-slate-500 dark:text-slate-400">
              共 {total} 条记录 · 第 {page} / {totalPages} 页
            </div>
            <div className="flex items-center gap-1">
              <button
                onClick={() => setPage(1)}
                disabled={page === 1}
                className="px-2 py-1 text-xs rounded disabled:opacity-50 hover:bg-slate-100 dark:hover:bg-slate-700"
              >
                首页
              </button>
              <button
                onClick={() => setPage((p) => Math.max(1, p - 1))}
                disabled={page === 1}
                className="px-2 py-1 text-xs rounded disabled:opacity-50 hover:bg-slate-100 dark:hover:bg-slate-700"
              >
                上一页
              </button>
              <span className="px-3 py-1 text-xs rounded bg-brand-100 text-brand-700 dark:bg-brand-900/30 dark:text-brand-300">
                {page}
              </span>
              <button
                onClick={() => setPage((p) => Math.min(totalPages, p + 1))}
                disabled={page >= totalPages}
                className="px-2 py-1 text-xs rounded disabled:opacity-50 hover:bg-slate-100 dark:hover:bg-slate-700"
              >
                下一页
              </button>
              <button
                onClick={() => setPage(totalPages)}
                disabled={page >= totalPages}
                className="px-2 py-1 text-xs rounded disabled:opacity-50 hover:bg-slate-100 dark:hover:bg-slate-700"
              >
                末页
              </button>
            </div>
          </div>
        )}
      </div>
    </div>
  )
}
