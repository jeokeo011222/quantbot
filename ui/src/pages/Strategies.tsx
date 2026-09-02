import { useState, useEffect, useRef } from 'react'
import { Target, Play, Pause, BarChart3, X, Check, Trash2, Eye, AlertCircle, Loader2 } from 'lucide-react'
import { getStrategies, refreshStrategyMetrics, toggleStrategyStatus as toggleStatusAPI, deleteStrategy as deleteStrategyAPI, type Strategy } from '../services/strategy'

interface StrategyForm {
  name: string
  description: string
  type: string
  capital: string
  maxPosition: string
  stopLoss: string
  takeProfit: string
  maWeight: string
  rsiWeight: string
  macdWeight: string
  volumeWeight: string
  kdjWeight: string
  buyThreshold: string
  sellThreshold: string
}

export default function Strategies() {
  const [strategies, setStrategies] = useState<Strategy[]>([])
  const [loading, setLoading] = useState(true)

  const [viewingStrategy, setViewingStrategy] = useState<Strategy | null>(null)
  const [deleteConfirm, setDeleteConfirm] = useState<Strategy | null>(null)
  const [actionFeedback, setActionFeedback] = useState<{ message: string; type: 'success' | 'error' } | null>(null)

  const [newStrategy, setNewStrategy] = useState<StrategyForm>({
    name: '',
    description: '',
    type: 'momentum',
    capital: '100000',
    maxPosition: '10',
    stopLoss: '5',
    takeProfit: '20',
    maWeight: '0.25',
    rsiWeight: '0.25',
    macdWeight: '0.25',
    volumeWeight: '0.15',
    kdjWeight: '0.10',
    buyThreshold: '0.6',
    sellThreshold: '0.4',
  })

  const strategyTypes = [
    { value: 'momentum', label: '动量策略' },
    { value: 'mean_reversion', label: '均值回归' },
    { value: 'arbitrage', label: '套利策略' },
    { value: 'ml_model', label: '机器学习' },
    { value: 'fundamental', label: '基本面因子' },
  ]

  const retryTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null)

  useEffect(() => {
    loadStrategies()
    return () => {
      if (retryTimerRef.current) clearTimeout(retryTimerRef.current)
    }
  }, [])

  const loadStrategies = async () => {
    setLoading(true)
    try {
      const list = await getStrategies()
      setStrategies(list)
    } catch (err) {
      console.error('Failed to load strategies:', err)
      showFeedback('加载策略列表失败，将在30秒后自动重试', 'error')
      // 失败后 30 秒自动重试，直到加载成功
      if (retryTimerRef.current) clearTimeout(retryTimerRef.current)
      retryTimerRef.current = setTimeout(loadStrategies, 30000)
    } finally {
      setLoading(false)
    }
  }

  // 刷新：触发后台真实回测刷新指标（而非仅重读 DB），紧接着重拉列表展示最新值
  const handleRefresh = async () => {
    setLoading(true)
    try {
      const res = await refreshStrategyMetrics()
      if (!res?.started) {
        showFeedback(res?.message || '指标刷新未开始', 'error')
      } else {
        showFeedback('已触发后台回测刷新指标，稍后自动更新')
      }
    } catch (err) {
      console.error('refreshStrategyMetrics failed:', err)
      showFeedback('触发指标刷新失败', 'error')
    }
    const list = await getStrategies()
    setStrategies(list)
    setLoading(false)
  }

  const showFeedback = (message: string, type: 'success' | 'error' = 'success') => {
    setActionFeedback({ message, type })
    setTimeout(() => setActionFeedback(null), 2500)
  }

  const getStatusColor = (status: string) => {
    if (status === 'running') return 'bg-green-50 text-green-700 dark:bg-green-900/30 dark:text-green-400'
    return 'bg-slate-100 text-slate-600 dark:bg-slate-700 dark:text-slate-400'
  }

  const getStatusText = (status: string) => {
    return status === 'running' ? '运行中' : '已停止'
  }

  const handleViewStrategy = (s: Strategy) => {
    setViewingStrategy(s)
  }

  const handleToggleStatus = async (s: Strategy) => {
    try {
      const updated = await toggleStatusAPI(s.id)
      if (updated) {
        setStrategies(prev => prev.map(strategy => 
          strategy.id === s.id ? updated : strategy
        ))
        setViewingStrategy(updated)
        const newStatus = updated.status === 'running' ? 'running' : 'stopped'
        showFeedback(`策略「${s.name}」已${newStatus === 'running' ? '启用' : '停止'}`)
      }
    } catch (err) {
      console.error('Failed to toggle status:', err)
      showFeedback('操作失败', 'error')
    }
  }

  const handleDeleteStrategy = async (s: Strategy) => {
    try {
      const success = await deleteStrategyAPI(s.id)
      if (success) {
        setStrategies(prev => prev.filter(strategy => strategy.id !== s.id))
        setDeleteConfirm(null)
        showFeedback(`策略「${s.name}」已删除`)
      } else {
        showFeedback('删除失败', 'error')
      }
    } catch (err) {
      console.error('Failed to delete strategy:', err)
      showFeedback('删除失败', 'error')
    }
  }

  return (
    <div className="space-y-6">
      {/* Feedback Toast */}
      {actionFeedback && (
        <div className={`fixed top-4 right-4 z-50 px-4 py-3 rounded-lg shadow-lg flex items-center gap-2 animate-fade-in ${
          actionFeedback.type === 'success'
            ? 'bg-green-600 text-white'
            : 'bg-red-600 text-white'
        }`}>
          <Check className="w-4 h-4" />
          <span className="text-sm font-medium">{actionFeedback.message}</span>
        </div>
      )}

      <div className="flex items-center justify-between">
        <div className="flex items-center gap-3">
          <div className="w-10 h-10 rounded-xl bg-gradient-to-br from-purple-500 to-indigo-500 flex items-center justify-center">
            <Target className="w-5 h-5 text-white" />
          </div>
          <h1 className="text-2xl font-bold text-slate-800 dark:text-slate-100">我的策略</h1>
        </div>
        <div className="flex items-center gap-2">
          <button
            onClick={handleRefresh}
            className="btn-secondary flex items-center gap-2"
            disabled={loading}
          >
            {loading ? <Loader2 className="w-4 h-4 animate-spin" /> : null}
            刷新
          </button>
        </div>
      </div>

      {loading ? (
        <div className="card p-12 text-center">
          <Loader2 className="w-8 h-8 mx-auto text-brand-500 animate-spin mb-4" />
          <p className="text-sm text-slate-500 dark:text-slate-400">加载策略列表...</p>
        </div>
      ) : strategies.length === 0 ? (
        <div className="card p-12 text-center">
          <Target className="w-16 h-16 mx-auto text-slate-300 dark:text-slate-600 mb-4" />
          <h3 className="text-lg font-medium text-slate-600 dark:text-slate-400 mb-2">暂无策略</h3>
          <p className="text-sm text-slate-400 dark:text-slate-500">系统预置的策略将在此处显示</p>
        </div>
      ) : (
        <div className="space-y-4">
          {strategies.map((s) => (
            <div key={s.id} className="card p-5">
              <div className="flex items-center justify-between mb-4">
                <div className="flex items-center gap-3">
                  <div className="w-10 h-10 rounded-lg bg-brand-50 dark:bg-brand-900/30 flex items-center justify-center">
                    <Target className="w-5 h-5 text-brand-500" />
                  </div>
                  <div>
                    <h3 className="font-semibold text-slate-800 dark:text-slate-100 flex items-center gap-2">
                      {s.name}
                      {s.is_builtin && (
                        <span className="inline-flex items-center px-2 py-0.5 rounded text-xs font-medium bg-indigo-100 text-indigo-700 dark:bg-indigo-900/30 dark:text-indigo-400">
                          内置
                        </span>
                      )}
                    </h3>
                    <div className="flex items-center gap-2 mt-1">
                      <span className={`inline-flex items-center px-2 py-0.5 rounded-full text-xs font-medium ${getStatusColor(s.status)}`}>
                        {getStatusText(s.status)}
                      </span>
                      <span className="text-xs text-slate-500 dark:text-slate-400">类型: {s.strategy_type}</span>
                    </div>
                  </div>
                </div>
                <div className="flex gap-2">
                  {/* View Button */}
                  <button
                    onClick={() => handleViewStrategy(s)}
                    className="p-2 rounded-lg hover:bg-slate-100 dark:hover:bg-slate-700 transition-colors"
                    title="查看策略详情"
                  >
                    <Eye className="w-4 h-4 text-slate-500 dark:text-slate-400" />
                  </button>
                  {/* Enable/Stop Button */}
                  <button
                    onClick={() => handleToggleStatus(s)}
                    className={`p-2 rounded-lg transition-colors ${
                      s.status === 'running'
                        ? 'hover:bg-yellow-50 dark:hover:bg-yellow-900/30'
                        : 'hover:bg-green-50 dark:hover:bg-green-900/30'
                    }`}
                    title={s.status === 'running' ? '停止策略' : '启用策略'}
                  >
                    {s.status === 'running' ? (
                      <Pause className="w-4 h-4 text-yellow-500" />
                    ) : (
                      <Play className="w-4 h-4 text-green-500" />
                    )}
                  </button>
                  {/* Delete Button - 内置策略不允许删除 */}
                  {!s.is_builtin && (
                    <button
                      onClick={() => setDeleteConfirm(s)}
                      className="p-2 rounded-lg hover:bg-red-50 dark:hover:bg-red-900/30 transition-colors"
                      title="删除策略"
                    >
                      <Trash2 className="w-4 h-4 text-red-500" />
                    </button>
                  )}
                </div>
              </div>

              <div className="grid grid-cols-4 gap-4 pt-3 border-t border-slate-200 dark:border-slate-700">
                <div>
                  <div className="text-xs text-slate-500 dark:text-slate-400">夏普比率</div>
                  <div className="text-lg font-semibold text-slate-800 dark:text-slate-100">{s.sharpe_ratio.toFixed(2)}</div>
                </div>
                <div>
                  <div className="text-xs text-slate-500 dark:text-slate-400">最大回撤</div>
                  <div className="text-lg font-semibold text-red-500">{s.max_drawdown.toFixed(2)}%</div>
                </div>
                <div>
                  <div className="text-xs text-slate-500 dark:text-slate-400">总收益率</div>
                  <div className={`text-lg font-semibold ${s.total_return >= 0 ? 'text-green-500' : 'text-red-500'}`}>{s.total_return.toFixed(2)}%</div>
                </div>
                <div>
                  <div className="text-xs text-slate-500 dark:text-slate-400">胜率</div>
                  <div className="text-lg font-semibold text-slate-800 dark:text-slate-100">{s.win_rate.toFixed(1)}%</div>
                </div>
              </div>

              {s.description && (
                <div className="mt-3 text-sm text-slate-600 dark:text-slate-400 bg-slate-50 dark:bg-slate-900/50 p-3 rounded-lg">
                  {s.description}
                </div>
              )}
            </div>
          ))}
        </div>
      )}

      {/* View Strategy Modal */}
      {viewingStrategy && (
        <div className="fixed inset-0 bg-black/50 flex items-center justify-center z-50 p-4" onClick={() => setViewingStrategy(null)}>
          <div
            className="bg-white dark:bg-slate-800 rounded-2xl max-w-md w-full max-h-[85vh] overflow-hidden"
            onClick={(e) => e.stopPropagation()}
          >
            <div className="flex items-center justify-between p-5 border-b border-slate-200 dark:border-slate-700">
              <div className="flex items-center gap-3">
                <BarChart3 className="w-5 h-5 text-brand-500" />
                <h2 className="text-lg font-semibold text-slate-800 dark:text-slate-100">策略详情</h2>
              </div>
              <button
                onClick={() => setViewingStrategy(null)}
                className="p-1.5 rounded-lg hover:bg-slate-100 dark:hover:bg-slate-700"
              >
                <X className="w-5 h-5 text-slate-500" />
              </button>
            </div>
            <div className="p-5 space-y-4">
              <div className="flex items-center gap-3">
                <div className="w-12 h-12 rounded-lg bg-brand-50 dark:bg-brand-900/30 flex items-center justify-center">
                  <Target className="w-6 h-6 text-brand-500" />
                </div>
                <div>
                  <h3 className="font-semibold text-slate-800 dark:text-slate-100">{viewingStrategy.name}</h3>
                  <div className="flex items-center gap-2 mt-1">
                    <span className={`inline-flex items-center px-2 py-0.5 rounded-full text-xs font-medium ${getStatusColor(viewingStrategy.status)}`}>
                      {getStatusText(viewingStrategy.status)}
                    </span>
                    <span className="text-xs text-slate-500 dark:text-slate-400">{viewingStrategy.strategy_type}</span>
                  </div>
                </div>
              </div>

              {viewingStrategy.description && (
                <div>
                  <div className="text-xs text-slate-500 dark:text-slate-400 mb-1">策略描述</div>
                  <div className="text-sm text-slate-700 dark:text-slate-300 bg-slate-50 dark:bg-slate-900/50 p-3 rounded-lg">
                    {viewingStrategy.description}
                  </div>
                </div>
              )}

              <div className="grid grid-cols-3 gap-3 pt-3 border-t border-slate-200 dark:border-slate-700">
                <div className="text-center p-3 bg-slate-50 dark:bg-slate-900/50 rounded-lg">
                  <div className="text-xs text-slate-500 dark:text-slate-400 mb-1">夏普比率</div>
                  <div className="text-lg font-semibold text-slate-800 dark:text-slate-100">{viewingStrategy.sharpe_ratio.toFixed(2)}</div>
                </div>
                <div className="text-center p-3 bg-slate-50 dark:bg-slate-900/50 rounded-lg">
                  <div className="text-xs text-slate-500 dark:text-slate-400 mb-1">最大回撤</div>
                  <div className="text-lg font-semibold text-red-500">{viewingStrategy.max_drawdown.toFixed(2)}%</div>
                </div>
                <div className="text-center p-3 bg-slate-50 dark:bg-slate-900/50 rounded-lg">
                  <div className="text-xs text-slate-500 dark:text-slate-400 mb-1">总收益率</div>
                  <div className={`text-lg font-semibold ${viewingStrategy.total_return >= 0 ? 'text-green-500' : 'text-red-500'}`}>{viewingStrategy.total_return.toFixed(2)}%</div>
                </div>
              </div>

              <div className="grid grid-cols-2 gap-3 text-xs">
                <div className="text-xs text-slate-500 dark:text-slate-400">
                  <div>初始资金: ¥{viewingStrategy.initial_capital.toLocaleString()}</div>
                </div>
                <div className="text-xs text-slate-500 dark:text-slate-400">
                  <div>最大持仓: {viewingStrategy.max_position} 只</div>
                </div>
                <div className="text-xs text-slate-500 dark:text-slate-400">
                  <div>止损线: {(viewingStrategy.stop_loss_pct * 100).toFixed(1)}%</div>
                </div>
                <div className="text-xs text-slate-500 dark:text-slate-400">
                  <div>止盈线: {(viewingStrategy.take_profit_pct * 100).toFixed(1)}%</div>
                </div>
              </div>

              <div className="text-xs text-slate-400 dark:text-slate-500 flex items-center gap-2">
                <span>策略 ID: {viewingStrategy.id}</span>
                <span className="text-slate-300 dark:text-slate-600">|</span>
                <span>创建时间: {new Date(viewingStrategy.created_at).toLocaleString('zh-CN')}</span>
              </div>
            </div>
            <div className="p-4 border-t border-slate-200 dark:border-slate-700 bg-slate-50 dark:bg-slate-900/50 flex justify-end gap-3">
              <button
                onClick={() => setViewingStrategy(null)}
                className="btn-secondary"
              >
                关闭
              </button>
              <button
                onClick={() => {
                  handleToggleStatus(viewingStrategy)
                }}
                className="btn-primary"
              >
                {viewingStrategy.status === 'running' ? '停止策略' : '启用策略'}
              </button>
            </div>
          </div>
        </div>
      )}

      {/* Delete Confirmation Modal */}
      {deleteConfirm && (
        <div className="fixed inset-0 bg-black/50 flex items-center justify-center z-50 p-4" onClick={() => setDeleteConfirm(null)}>
          <div
            className="bg-white dark:bg-slate-800 rounded-2xl max-w-sm w-full p-6"
            onClick={(e) => e.stopPropagation()}
          >
            <div className="flex items-center gap-3 mb-4">
              <div className="w-10 h-10 rounded-full bg-red-100 dark:bg-red-900/30 flex items-center justify-center">
                <AlertCircle className="w-5 h-5 text-red-500" />
              </div>
              <div>
                <h3 className="font-semibold text-slate-800 dark:text-slate-100">确认删除</h3>
                <p className="text-sm text-slate-500 dark:text-slate-400">此操作不可撤销</p>
              </div>
            </div>
            <p className="text-sm text-slate-700 dark:text-slate-300 mb-6">
              确定要删除策略「<span className="font-medium text-slate-800 dark:text-slate-100">{deleteConfirm.name}</span>」吗？
            </p>
            <div className="flex justify-end gap-3">
              <button
                onClick={() => setDeleteConfirm(null)}
                className="btn-secondary"
              >
                取消
              </button>
              <button
                onClick={() => handleDeleteStrategy(deleteConfirm)}
                className="px-4 py-2 rounded-lg bg-red-500 text-white hover:bg-red-600 transition-colors font-medium text-sm"
              >
                删除
              </button>
            </div>
          </div>
        </div>
      )}

    </div>
  )
}
