// API 公共辅助模块 - 统一管理后端 Wails API 调用
// 减少代码重复，提高类型安全性

// App 方法接口
export interface AppAPI {
  [key: string]: (...args: unknown[]) => Promise<unknown> | unknown
}

// App 实例缓存
let appInstance: AppAPI | null = null

/**
 * 获取 Wails App 实例
 * 统一入口，缓存实例以提高性能
 */
export function getAppInstance(): AppAPI | null {
  if (appInstance !== null) {
    return appInstance
  }

  try {
    const app = (window as unknown as { go?: { main?: { App?: AppAPI } } })['go']?.['main']?.['App']
    if (app && typeof app === 'object') {
      appInstance = app as AppAPI
      return appInstance
    }
  } catch (e) {
    console.warn('[API] Failed to get App instance:', e)
  }

  return null
}

/**
 * 安全调用后端 API 方法
 * @param methodName 方法名
 * @param args 参数列表
 * @param fallbackValue 失败时返回的默认值
 * @param onError 可选回调，后端真正抛错时触发，用于区分"后端异常"与"没有数据"，
 *                避免把后端异常静默吞成空数组/默认值而误显"无数据"。
 */
export async function safeCall<T>(
  methodName: string,
  args: unknown[],
  fallbackValue: T,
  onError?: (err: unknown) => void,
): Promise<T> {
  const app = getAppInstance()
  if (!app || typeof app[methodName] !== 'function') {
    // 后端/方法本身不可用：明确记录，避免静默按"数据为空"处理
    const err = new Error(`Backend method '${methodName}' not available`)
    console.error(`[API] ${methodName} unavailable:`, err)
    if (onError) onError(err)
    return fallbackValue
  }

  try {
    const method = app[methodName] as (...args: unknown[]) => Promise<T> | T
    const result = await method(...args)
    return result
  } catch (err) {
    // 后端异常：不得吞掉并按"空数据"展示；交由 onError 上报，仍返回 fallback 避免崩溃
    console.error(`[API] ${methodName} failed:`, err)
    if (onError) onError(err)
    return fallbackValue
  }
}

/**
 * 检查后端连接状态
 */
export function isBackendAvailable(): boolean {
  return getAppInstance() !== null
}

/**
 * 重置 App 实例缓存（用于热更新场景）
 */
export function resetAppInstance(): void {
  appInstance = null
}
