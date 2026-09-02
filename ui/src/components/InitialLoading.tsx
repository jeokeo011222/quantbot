import { useState, useEffect, useCallback } from 'react'
import { IsReady } from '../../wailsjs/go/main/App'
import AppLogo from './AppLogo'

interface Props {
  onReady: () => void
}

interface SystemStatus {
  ready: boolean
  config_ready: boolean
  sqlite_ready: boolean
  duckdb_ready: boolean
  conn_db_ok: boolean
  conn_db_detail: string
  conn_ds_ok: boolean
  conn_ds_detail: string
  conn_ai_ready: boolean
  conn_ai_error: string
  portfolio_ready: boolean
  portfolio_warm: boolean
  portfolio_warm_error: string
  trading_hours: boolean
  agent_executor_ready: boolean
  llm_ready: boolean
  errors: string[]
  log_path?: string
}

export default function InitialLoading({ onReady }: Props) {
  const [status, setStatus] = useState<SystemStatus>({
    ready: false,
    config_ready: false,
    sqlite_ready: false,
    duckdb_ready: false,
    conn_db_ok: false,
    conn_db_detail: '',
    conn_ds_ok: false,
    conn_ds_detail: '',
    conn_ai_ready: false,
    conn_ai_error: '',
    portfolio_ready: false,
    portfolio_warm: false,
    portfolio_warm_error: '',
    trading_hours: false,
    agent_executor_ready: false,
    llm_ready: false,
    errors: [],
  })
  const [progress, setProgress] = useState(0)
  const [currentStep, setCurrentStep] = useState('正在初始化系统...')
  const [elapsedTime, setElapsedTime] = useState(0)
  const [initFailed, setInitFailed] = useState(false)
  const [showContinue, setShowContinue] = useState(false)

  const checkReady = useCallback(async () => {
    try {
      const result = await IsReady()
      if (result) {
        const newStatus: SystemStatus = {
          ready: result.ready || false,
          config_ready: result.config_ready || false,
          sqlite_ready: result.sqlite_ready || false,
          duckdb_ready: result.duckdb_ready || false,
          conn_db_ok: result.conn_db_ok || false,
          conn_db_detail: result.conn_db_detail || '',
          conn_ds_ok: result.conn_ds_ok || false,
          conn_ds_detail: result.conn_ds_detail || '',
          conn_ai_ready: result.conn_ai_ready || false,
          conn_ai_error: result.conn_ai_error || '',
          portfolio_ready: result.portfolio_ready || false,
          portfolio_warm: result.portfolio_warm || false,
          portfolio_warm_error: result.portfolio_warm_error || '',
          trading_hours: result.trading_hours || false,
          agent_executor_ready: result.agent_executor_ready || false,
          llm_ready: result.llm_ready || false,
          errors: result.errors || [],
          log_path: result.log_path || '',
        }
        setStatus(newStatus)
        // 组合预热明确失败时，提前终止等待并报错（不降级、不静默等到超时）
        if (!newStatus.ready && newStatus.portfolio_warm_error) {
          setInitFailed(true)
          setShowContinue(true)
          setCurrentStep('投资组合数据预热失败')
        }
        return newStatus.ready
      }
    } catch {
      // API 调用失败
    }
    return false
  }, [])

  useEffect(() => {
    const startTime = Date.now()
    const maxWaitTime = 30000 // 最大等待30秒

    const initialize = async () => {
      const timer = setInterval(() => {
        setElapsedTime(Math.floor((Date.now() - startTime) / 1000))
        setProgress(Math.min((Date.now() - startTime) / maxWaitTime * 100, 95))
      }, 500)

      const pollInterval = setInterval(async () => {
        const ready = await checkReady()
        if (ready) {
          clearInterval(pollInterval)
          clearInterval(timer)
          setProgress(100)
          setCurrentStep('系统初始化完成')
          setTimeout(() => {
            onReady()
          }, 500)
        } else if (Date.now() - startTime > maxWaitTime) {
          clearInterval(pollInterval)
          clearInterval(timer)
          setProgress(100)
          setInitFailed(true)
          setShowContinue(true)
          setCurrentStep('系统初始化失败')
        }
      }, 1000)

      // 立即检查一次
      const ready = await checkReady()
      if (ready) {
        clearInterval(pollInterval)
        setProgress(100)
        setCurrentStep('系统初始化完成')
        setTimeout(() => {
          onReady()
        }, 500)
      }
    }

    initialize()

    return () => {
      // 清理
    }
  }, [checkReady, onReady])

  const componentStatus = [
    { key: 'config', name: '配置管理器', ready: status.config_ready, critical: true },
    { key: 'sqlite', name: 'SQLite 数据库', ready: status.conn_db_ok, critical: true },
    { key: 'duckdb', name: '股票行情数据', ready: status.conn_ds_ok, critical: true },
    { key: 'llm', name: 'LLM 大模型服务', ready: status.conn_ai_ready, critical: true },
    { key: 'portfolio', name: '投资组合引擎', ready: status.portfolio_ready, critical: false },
    { key: 'portfolio_warm', name: '组合数据预热', ready: status.portfolio_warm, critical: true },
    { key: 'executor', name: '智能体执行器', ready: status.agent_executor_ready, critical: false },
  ]

  const criticalFailed = componentStatus.some(c => c.critical && !c.ready)

  const warmError = status.portfolio_warm_error || (status.portfolio_ready && !status.portfolio_warm ? '组合数据预热中...' : '')

  return (
    <div className="fixed inset-0 z-[9999] flex items-center justify-center bg-slate-900/95 backdrop-blur-sm">
      <div className="w-full max-w-md mx-4">
        {/* Logo 和标题 */}
        <div className="text-center mb-8">
          <div className="mx-auto mb-4 w-20 h-20">
            <AppLogo
              size={80}
              rounded="rounded-2xl"
              className={initFailed ? 'opacity-60' : 'animate-pulse'}
            />
          </div>
          <h1 className="text-2xl font-bold text-white mb-1">QuantBot</h1>
          <p className="text-slate-400 text-sm">AI量化机器人</p>
        </div>

        {/* 进度条 */}
        <div className="mb-6">
          <div className="h-2 bg-slate-700 rounded-full overflow-hidden">
            <div
              className={`h-full transition-all duration-300 ${initFailed ? 'bg-gradient-to-r from-red-500 to-orange-400' : 'bg-gradient-to-r from-blue-500 to-cyan-400'}`}
              style={{ width: `${progress}%` }}
            />
          </div>
          <div className="flex justify-between mt-2 text-xs">
            <span className={initFailed ? 'text-red-400' : 'text-slate-400'}>{currentStep}</span>
            <span className="text-slate-500">{elapsedTime}s</span>
          </div>
        </div>

        {/* 组件初始化状态 */}
        <div className="space-y-2">
          {componentStatus.map((component) => (
            <div
              key={component.key}
              className={`flex items-center justify-between p-3 rounded-lg border transition-all duration-300 ${
                component.ready
                  ? 'bg-green-900/20 border-green-500/30'
                  : 'bg-slate-800/50 border-slate-700'
              }`}
            >
              <div className="flex items-center gap-3">
                <div
                  className={`w-5 h-5 rounded-full flex items-center justify-center ${
                    component.ready ? 'bg-green-500' : component.critical && initFailed ? 'bg-red-500' : 'bg-slate-600'
                  }`}
                >
                  {component.ready ? (
                    <svg className="w-3 h-3 text-white" fill="none" stroke="currentColor" viewBox="0 0 24 24">
                      <path strokeLinecap="round" strokeLinejoin="round" strokeWidth={3} d="M5 13l4 4L7 7" />
                    </svg>
                  ) : component.critical && initFailed ? (
                    <svg className="w-3 h-3 text-white" fill="none" stroke="currentColor" viewBox="0 0 24 24">
                      <path strokeLinecap="round" strokeLinejoin="round" strokeWidth={3} d="M6 18L18 6M6 6l12 12" />
                    </svg>
                  ) : (
                    <div className="w-2 h-2 bg-slate-400 rounded-full animate-pulse" />
                  )}
                </div>
                <div className="flex items-center gap-2">
                  <span
                    className={`text-sm ${
                      component.ready ? 'text-green-400' : 'text-slate-300'
                    }`}
                  >
                    {component.name}
                  </span>
                </div>
              </div>
              <span
                className={`text-xs font-medium ${
                  component.ready ? 'text-green-400' : component.critical && initFailed ? 'text-red-400' : 'text-slate-500'
                }`}
              >
                {component.ready ? '就绪' : component.critical && initFailed ? '失败' : '初始化中...'}
              </span>
            </div>
          ))}
        </div>

        {/* 加载提示 */}
        {!initFailed && (
          <div className="mt-6 text-center">
            <p className="text-slate-500 text-xs">
              系统正在启动，请稍候...
            </p>
            <div className="flex justify-center gap-1 mt-2">
              <span className="w-2 h-2 bg-slate-500 rounded-full animate-bounce" style={{ animationDelay: '0ms' }} />
              <span className="w-2 h-2 bg-slate-500 rounded-full animate-bounce" style={{ animationDelay: '150ms' }} />
              <span className="w-2 h-2 bg-slate-500 rounded-full animate-bounce" style={{ animationDelay: '300ms' }} />
            </div>
          </div>
        )}

        {/* 错误信息 - 仅在关键组件失败时显示 */}
        {initFailed && criticalFailed ? (
          <div className="mt-4 p-3 rounded-lg bg-red-900/20 border border-red-500/30">
            <p className="text-red-400 text-xs font-medium mb-1">
              系统初始化失败
            </p>
            <p className="text-red-300 text-xs">
              核心组件初始化失败，请检查配置后重启。
            </p>
            {status.errors && status.errors.length > 0 && (
              <div className="mt-2 text-xs text-red-300 space-y-1">
                {status.errors.slice(0, 3).map((err, idx) => (
                  <p key={idx}>• {err}</p>
                ))}
              </div>
            )}
          </div>
        ) : null}

        {/* 投资组合数据预热错误 - 明确报错，不降级 */}
        {!initFailed && status.portfolio_warm_error ? (
          <div className="mt-4 p-3 rounded-lg bg-red-900/20 border border-red-500/30">
            <p className="text-red-400 text-xs font-medium mb-1">投资组合数据预热失败</p>
            <p className="text-red-300 text-xs leading-relaxed">{status.portfolio_warm_error}</p>
          </div>
        ) : null}

        {/* 继续按钮（初始化失败后） */}
        {showContinue && (
          <div className="mt-4 text-center">
            <button
              onClick={() => onReady()}
              className="px-6 py-2 bg-red-600 hover:bg-red-700 text-white text-sm font-medium rounded-lg transition-colors"
            >
              进入系统
            </button>
          </div>
        )}
      </div>
    </div>
  )
}
