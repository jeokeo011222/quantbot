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
 */
export async function safeCall<T>(
  methodName: string,
  args: unknown[],
  fallbackValue: T,
): Promise<T> {
  const app = getAppInstance()
  if (!app || typeof app[methodName] !== 'function') {
    return fallbackValue
  }

  try {
    const method = app[methodName] as (...args: unknown[]) => Promise<T> | T
    const result = await method(...args)
    return result
  } catch (err) {
    console.error(`[API] ${methodName} failed:`, err)
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
