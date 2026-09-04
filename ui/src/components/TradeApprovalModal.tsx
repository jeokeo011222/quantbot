import { useState, useEffect, useCallback, useRef } from 'react'
import { X, ShoppingCart, AlertTriangle, Clock } from 'lucide-react'
import { EventsOn, EventsOff } from '../../wailsjs/runtime/runtime'
import { ConfirmTradeApproval, GetPendingTradeApprovals } from '../../wailsjs/go/main/App'
import { formatCurrency } from '../utils/formatters'

interface PendingTrade {
  id: string
  action: 'BUY' | 'SELL'
  symbol: string
  stock_name: string
  market: string
  quantity: number
  price: number
  amount: number
  reason: string
  decision_id: string
  created_at: string
  status: string
}

export default function TradeApprovalModal() {
  const [queue, setQueue] = useState<PendingTrade[]>([])
  const [processing, setProcessing] = useState(false)
  const [error, setError] = useState<string>('')
  const current = queue[0] || null
  const queueRef = useRef<PendingTrade[]>([])

  const syncQueue = useCallback((list: PendingTrade[]) => {
    queueRef.current = list
    setQueue([...list])
  }, [])

  // 挂载时加载后端已有的待确认交易（防止事件在组件挂载前触发而丢失）
  useEffect(() => {
    let cancelled = false
    GetPendingTradeApprovals()
      .then((list: PendingTrade[]) => {
        if (cancelled || !Array.isArray(list)) return
        const fresh = list.filter((t) => t?.id && !queueRef.current.some((q) => q.id === t.id))
        if (fresh.length > 0) syncQueue([...queueRef.current, ...fresh])
      })
      .catch(() => {})
    return () => {
      cancelled = true
    }
  }, [syncQueue])

  // 监听后端交易审批请求事件
  useEffect(() => {
    const off = EventsOn('trade:approval_request', (trade: PendingTrade) => {
      if (!trade?.id) return
      // 避免重复加入
      if (queueRef.current.some((t) => t.id === trade.id)) return
      syncQueue([...queueRef.current, trade])
    })
    return () => {
      off()
      EventsOff('trade:approval_request')
    }
  }, [syncQueue])

  // 监听日终结算收盘清扫：未获确认的排队交易判定失败，自动从弹窗队列移除
  useEffect(() => {
    const off = EventsOn('trade:approval_expired', (trades: PendingTrade[] | any) => {
      if (!Array.isArray(trades) || trades.length === 0) return
      const ids = new Set(trades.map((t: PendingTrade) => t.id).filter(Boolean))
      if (ids.size === 0) return
      syncQueue(queueRef.current.filter((t) => !ids.has(t.id)))
    })
    return () => {
      off()
      EventsOff('trade:approval_expired')
    }
  }, [syncQueue])

  const handleConfirm = async (approved: boolean) => {
    if (!current || processing) return
    const tradeId = current.id
    setProcessing(true)
    setError('')
    try {
      await ConfirmTradeApproval(tradeId, approved)
      syncQueue(queueRef.current.filter((t) => t.id !== tradeId))
    } catch (e: any) {
      const errMsg = e?.message || '确认失败，请重试'
      setError(errMsg)
      // 关键修复：即使API失败（如后端交易已超时/不存在/已收盘），也从本地队列移除，防止弹窗卡死
      // 错误信息包含"不存在或已处理"时，说明后端已无此交易，前端应同步关闭
      if (errMsg.includes('不存在') || errMsg.includes('已处理') || errMsg.includes('超时') || errMsg.includes('已收盘') || errMsg.includes('判定失败') || errMsg.includes('not found') || errMsg.includes('already')) {
        syncQueue(queueRef.current.filter((t) => t.id !== tradeId))
      }
    } finally {
      setProcessing(false)
    }
  }

  if (!current) return null

  const isBuy = current.action === 'BUY'

  return (
    <div className="fixed inset-0 z-[9998] flex items-center justify-center bg-slate-900/60 backdrop-blur-sm p-4">
      <div className="w-full max-w-md bg-white dark:bg-slate-800 rounded-2xl shadow-2xl border border-slate-200 dark:border-slate-700 overflow-hidden">
        {/* 头部 */}
        <div className={`px-5 py-4 flex items-center justify-between ${isBuy ? 'bg-red-500' : 'bg-green-500'}`}>
          <div className="flex items-center gap-2 text-white">
            <ShoppingCart className="w-5 h-5" />
            <h3 className="font-medium text-sm">智能体交易确认</h3>
          </div>
          <span className="inline-flex items-center gap-1 text-xs text-white/90">
            <Clock className="w-3.5 h-3.5" />
            等待确认
          </span>
        </div>

        <div className="p-5 space-y-4">
          {/* 交易摘要 */}
          <div className="p-3 bg-slate-50 dark:bg-slate-700/50 rounded-lg">
            <div className="flex items-center justify-between mb-2">
              <span className="text-xs text-slate-500 dark:text-slate-400">交易方向</span>
              <span className={`text-sm font-bold ${isBuy ? 'text-red-600 dark:text-red-400' : 'text-green-600 dark:text-green-400'}`}>
                {isBuy ? '买入' : '卖出'}
              </span>
            </div>
            <div className="flex items-center justify-between mb-2">
              <span className="text-xs text-slate-500 dark:text-slate-400">股票</span>
              <span className="text-sm font-medium text-slate-800 dark:text-slate-100 font-mono">
                {current.stock_name || current.symbol} ({current.symbol})
              </span>
            </div>
            <div className="flex items-center justify-between mb-2">
              <span className="text-xs text-slate-500 dark:text-slate-400">数量</span>
              <span className="text-sm font-medium text-slate-800 dark:text-slate-100">{current.quantity} 股</span>
            </div>
            <div className="flex items-center justify-between mb-2">
              <span className="text-xs text-slate-500 dark:text-slate-400">价格</span>
              <span className="text-sm font-medium text-slate-800 dark:text-slate-100">¥{current.price.toFixed(2)}</span>
            </div>
            <div className="flex items-center justify-between">
              <span className="text-xs text-slate-500 dark:text-slate-400">交易金额</span>
              <span className="text-sm font-bold text-slate-800 dark:text-slate-100">
                {formatCurrency(current.amount || current.price * current.quantity)}
              </span>
            </div>
          </div>

          {/* 原因 */}
          {current.reason && (
            <div className="p-3 bg-slate-50 dark:bg-slate-700/50 rounded-lg">
              <div className="flex items-center gap-1.5 mb-1">
                <AlertTriangle className="w-3.5 h-3.5 text-amber-500" />
                <span className="text-xs font-medium text-slate-600 dark:text-slate-300">交易原因</span>
              </div>
              <p className="text-xs text-slate-600 dark:text-slate-300 leading-relaxed">{current.reason}</p>
            </div>
          )}

          {/* 决策ID */}
          {current.decision_id && (
            <div className="text-[11px] text-slate-400 dark:text-slate-500 font-mono break-all">
              决策ID: {current.decision_id}
            </div>
          )}

          {error && (
            <div className="p-2.5 rounded-md text-xs bg-red-50 dark:bg-red-900/20 text-red-700 dark:text-red-400 border border-red-200 dark:border-red-800">
              {error}
            </div>
          )}

          {/* 操作按钮 */}
          <div className="grid grid-cols-2 gap-3 pt-1">
            <button
              onClick={() => handleConfirm(false)}
              disabled={processing}
              className="py-2.5 rounded-lg text-sm font-medium bg-slate-200 dark:bg-slate-700 text-slate-700 dark:text-slate-200 hover:bg-slate-300 dark:hover:bg-slate-600 disabled:opacity-50 transition-colors"
            >
              拒绝
            </button>
            <button
              onClick={() => handleConfirm(true)}
              disabled={processing}
              className={`py-2.5 rounded-lg text-sm font-medium text-white disabled:opacity-50 transition-colors ${
                isBuy ? 'bg-red-500 hover:bg-red-600' : 'bg-green-500 hover:bg-green-600'
              }`}
            >
              {processing ? '处理中...' : isBuy ? '确认买入' : '确认卖出'}
            </button>
          </div>

          <p className="text-[11px] text-slate-400 dark:text-slate-500 text-center">
            模拟接口模式下，智能体交易需人工确认。未及时确认将保持排队等待，可稍后确认/拒绝；确认结果决定订单是否成交。
          </p>
        </div>
      </div>
    </div>
  )
}
