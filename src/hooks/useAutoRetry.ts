import { useCallback, useEffect, useRef } from 'react'
import { useToastStore } from '../store/toastStore'

export interface AutoRetryOptions {
  /** 重试间隔（毫秒），默认 30000 */
  retryMs?: number
  /** 弹窗标题，默认 '数据获取失败，将在30秒后自动重试' */
  title?: string
  /** 从异常中提取提示，默认取 err.message */
  getMessage?: (err: unknown) => string
}

/**
 * 数据获取失败的统一处理：首次失败弹窗警示，之后每 retryMs 毫秒自动重试，
 * 直到成功为止（成功后停止重试）。组件卸载时自动清理定时器。
 *
 * 用法：
 *   const { start, stop } = useAutoRetry()
 *   useEffect(() => {
 *     start(() => fetchSomething().then((d) => setData(d)))
 *     return () => stop()
 *   }, [start, stop])
 */
export function useAutoRetry(opts: AutoRetryOptions = {}) {
  const retryMs = opts.retryMs ?? 30000
  const timerRef = useRef<ReturnType<typeof setTimeout> | null>(null)
  const failNotifiedRef = useRef(false)
  const getMessageRef = useRef(opts.getMessage)
  const titleRef = useRef(opts.title)
  getMessageRef.current = opts.getMessage
  titleRef.current = opts.title

  const start = useCallback(
    (task: () => Promise<unknown>) => {
      if (timerRef.current) {
        clearTimeout(timerRef.current)
        timerRef.current = null
      }
      task()
        .then(() => {
          failNotifiedRef.current = false
        })
        .catch((err) => {
          if (!failNotifiedRef.current) {
            failNotifiedRef.current = true
            const msg = getMessageRef.current
              ? getMessageRef.current(err)
              : err instanceof Error
              ? err.message
              : String(err)
            useToastStore.getState().error(
              titleRef.current || '数据获取失败，将在30秒后自动重试',
              msg
            )
          }
          timerRef.current = setTimeout(() => start(task), retryMs)
        })
    },
    [retryMs]
  )

  const stop = useCallback(() => {
    if (timerRef.current) {
      clearTimeout(timerRef.current)
      timerRef.current = null
    }
  }, [])

  useEffect(() => stop, [stop])

  return { start, stop }
}