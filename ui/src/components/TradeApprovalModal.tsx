import { useState, useEffect, useCallback, useRef } from 'react'
import { X, ShoppingCart, AlertTriangle, Clock, ShieldCheck, ShieldAlert } from 'lucide-react'
import { EventsOn, EventsOff } from '../../wailsjs/runtime/runtime'
import { ConfirmTradeApproval, GetPendingTradeApprovals } from '../../wailsjs/go/main/App'
import { formatCurrency } from '../utils/formatters'

// ReasonCode → 中文标签映射，镜像后端 reasoning.CodeLabels。
// 前端据此渲染稳定原因码为可读标签，避免依赖自由文本 reason 做展示。
// 新增 code 必须同步更新此表（与后端 internal/reasoning/reason.go 对齐）。
const REASON_CODE_LABELS: Record<string, string> = {
  ALPHA_BUY: 'AlphaScore 触发买入阈值',
  ALPHA_SELL: 'AlphaScore 触发卖出阈值',
  ALPHA_WEAK: 'AlphaScore 未达阈值（持有观望）',
  SIGNAL_STRONG: '信号强度加分',
  SIGNAL_WEAK: '信号强度不足',
  MAX_DRAWDOWN_EXCEEDED: '组合回撤超过投资指引上限',
  VAR_HIGH: 'VaR99 偏高',
  HIGH_VOLATILITY: '波动率过高',
  LOW_LIQUIDITY: '流动性不足',
  HIGH_CONCENTRATION: '集中度风险过高',
  CRITICAL_RISK_STATUS: '风控状态 CRITICAL',
  MANDATE_DRAWDOWN: '投资指引回撤约束',
  MANDATE_RISK_LEVEL: '投资指引风险等级不匹配',
  MANDATE_CASH_RATIO: '投资指引现金比例约束',
  MANDATE_SECTOR_LIMIT: '投资指引行业集中度约束',
  MANDATE_SINGLE_POS: '投资指引单标的仓位上限',
  OVERALL_RISK_TOO_HIGH: '组合整体风险超阈值（买入降级）',
  APPROVED_BY_USER: '用户审批通过',
  REJECTED_BY_USER: '用户审批拒绝',
  APPROVAL_TIMEOUT: '用户审批超时',
  EXPIRED_AT_CLOSE: '15:00 收盘未获确认',
  EXECUTION_FAILED: '交易执行失败',
  BELOW_CONFIDENCE: '置信度偏低，建议谨慎操作',
  ABOVE_TARGET_WEIGHT: '目标权重偏高',
  SELL_GUARD_DEDUP: '卖出护栏：同日去重',
  SELL_GUARD_HYSTERESIS: '卖出护栏：滞回灰区',
  SELL_GUARD_STREAK: '卖出护栏：连续 N 次未满足',
  DRAWDOWN_WARN: '净值回撤触及预警线',
  DRAWDOWN_STOP: '净值回撤触及熔断线',
  DUPLICATE: '重复事件已去重',
  RETRY: '重试后成功',
  MANUAL_OVERRIDE: '人工覆盖自动决策',
}

const reasonLabelOf = (code: string) => REASON_CODE_LABELS[code] || code

// HonestStatus → 标签 + 颜色
const HONEST_STATUS_STYLES: Record<string, { label: string; cls: string }> = {
  draft: { label: '草稿', cls: 'bg-slate-100 text-slate-600 dark:bg-slate-700 dark:text-slate-300' },
  pending: { label: '等待确认', cls: 'bg-amber-100 text-amber-700 dark:bg-amber-900/30 dark:text-amber-400' },
  verified: { label: '已确认', cls: 'bg-emerald-100 text-emerald-700 dark:bg-emerald-900/30 dark:text-emerald-400' },
  rejected: { label: '已拒绝', cls: 'bg-rose-100 text-rose-700 dark:bg-rose-900/30 dark:text-rose-400' },
  superseded: { label: '已更新', cls: 'bg-slate-100 text-slate-600 dark:bg-slate-700 dark:text-slate-300' },
  failed: { label: '已失败', cls: 'bg-rose-100 text-rose-700 dark:bg-rose-900/30 dark:text-rose-400' },
}

const honestStatusOf = (s: string) => HONEST_STATUS_STYLES[s] || HONEST_STATUS_STYLES.pending

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
  // 结构化原因码（权威来源，吸收 dsh-superagent 结构化 Reason 原则）
  reason_codes?: { code: string; values?: Record<string, any>; text?: string }[]
  // 诚实汇报状态标签（draft/pending/verified/rejected/failed）
  honest_status?: string
  // humanaction 对齐 ID
  human_action_id?: string
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

  // 诚实状态徽标：吸收 dsh-superagent "诚实汇报"原则
  const hs = honestStatusOf(current.honest_status || current.status)

  // 结构化原因码（权威来源）
  const reasonCodes = current.reason_codes && current.reason_codes.length > 0
    ? current.reason_codes
    : null

  return (
    <div className="fixed inset-0 z-[9998] flex items-center justify-center bg-slate-900/60 backdrop-blur-sm p-4">
      <div className="w-full max-w-md bg-white dark:bg-slate-800 rounded-2xl shadow-2xl border border-slate-200 dark:border-slate-700 overflow-hidden">
        {/* 头部 */}
        <div className={`px-5 py-4 flex items-center justify-between ${isBuy ? 'bg-red-500' : 'bg-green-500'}`}>
          <div className="flex items-center gap-2 text-white">
            <ShoppingCart className="w-5 h-5" />
            <h3 className="font-medium text-sm">智能体交易确认</h3>
          </div>
          <div className="flex items-center gap-2 text-white">
            {/* 诚实状态徽标：pending/verified/rejected */}
            {(() => {
              const Icon = current.honest_status === 'verified'
                ? ShieldCheck
                : current.honest_status === 'rejected' || current.honest_status === 'failed'
                ? ShieldAlert
                : Clock
              return (
                <span className={`inline-flex items-center gap-1 text-[11px] px-1.5 py-0.5 rounded ${hs.cls} text-white/95 dark:text-white/95`}>
                  <Icon className="w-3 h-3" />
                  {hs.label}
                </span>
              )
            })()}
          </div>
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

          {/* 结构化原因码（权威来源，吸收 dsh-superagent 结构化 Reason 原则） */}
          {reasonCodes && (
            <div className="p-3 bg-slate-50 dark:bg-slate-700/50 rounded-lg">
              <div className="flex items-center gap-1.5 mb-2">
                <ShieldCheck className="w-3.5 h-3.5 text-emerald-500" />
                <span className="text-xs font-medium text-slate-600 dark:text-slate-300">触发条件</span>
                <span className="text-[10px] text-slate-400 dark:text-slate-500">(结构化，权威)</span>
              </div>
              <div className="flex flex-wrap gap-1.5">
                {reasonCodes.map((r, idx) => (
                  <span
                    key={`${r.code}-${idx}`}
                    title={r.text || reasonLabelOf(r.code)}
                    className="inline-flex items-center gap-1 text-[11px] px-2 py-1 rounded-md bg-indigo-100 text-indigo-700 dark:bg-indigo-900/30 dark:text-indigo-300 font-mono"
                  >
                    <span className="font-semibold">{reasonLabelOf(r.code)}</span>
                  </span>
                ))}
              </div>
            </div>
          )}

          {/* 自由文本原因（兜底展示，不作为程序输入） */}
          {current.reason && (
            <div className="p-3 bg-slate-50 dark:bg-slate-700/50 rounded-lg">
              <div className="flex items-center gap-1.5 mb-1">
                <AlertTriangle className="w-3.5 h-3.5 text-amber-500" />
                <span className="text-xs font-medium text-slate-600 dark:text-slate-300">交易原因</span>
                {reasonCodes && (
                  <span className="text-[10px] text-slate-400 dark:text-slate-500">(兜底展示)</span>
                )}
              </div>
              <p className="text-xs text-slate-600 dark:text-slate-300 leading-relaxed">{current.reason}</p>
            </div>
          )}

          {/* 决策ID / humanaction_id */}
          {(current.decision_id || current.human_action_id) && (
            <div className="text-[11px] text-slate-400 dark:text-slate-500 font-mono break-all space-y-0.5">
              {current.decision_id && <div>决策ID: {current.decision_id}</div>}
              {current.human_action_id && current.human_action_id !== current.decision_id && (
                <div>HumanAction: {current.human_action_id}</div>
              )}
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
