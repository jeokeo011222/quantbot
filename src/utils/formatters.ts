// 数字和金额格式化工具

/**
 * 格式化金额显示
 */
export function formatCurrency(value: number, _currency: string = 'CNY'): string {
  if (value === null || value === undefined || isNaN(value)) return '-'
  const absVal = Math.abs(value)
  let formatted: string
  if (absVal >= 10000) {
    formatted = `¥${(value / 10000).toFixed(2)}万`
  } else {
    formatted = `¥${value.toLocaleString('zh-CN', { minimumFractionDigits: 2, maximumFractionDigits: 2 })}`
  }
  return formatted
}

/**
 * 格式化金额（详细格式）
 */
export function formatFullCurrency(value: number): string {
  if (value === null || value === undefined || isNaN(value)) return '-'
  return `¥${value.toLocaleString('zh-CN', { minimumFractionDigits: 2, maximumFractionDigits: 2 })}`
}

/**
 * 格式化百分比
 */
export function formatPercent(value: number, decimals: number = 2): string {
  if (value === null || value === undefined || isNaN(value)) return '-'
  const prefix = value > 0 ? '+' : ''
  return `${prefix}${value.toFixed(decimals)}%`
}

/**
 * 格式化带颜色的百分比（返回 Tailwind class）
 */
export function getPnLColor(value: number): string {
  if (value > 0) return 'text-green-600 dark:text-green-400'
  if (value < 0) return 'text-red-600 dark:text-red-400'
  return 'text-slate-500 dark:text-slate-400'
}

/**
 * 格式化数字
 */
export function formatNumber(value: number, decimals: number = 2): string {
  if (value === null || value === undefined || isNaN(value)) return '-'
  return value.toLocaleString('zh-CN', { minimumFractionDigits: decimals, maximumFractionDigits: decimals })
}

/**
 * 格式化大数字（带单位）
 */
export function formatLargeNumber(value: number): string {
  if (value === null || value === undefined || isNaN(value)) return '-'
  const abs = Math.abs(value)
  if (abs >= 100000000) {
    return `${(value / 100000000).toFixed(2)}亿`
  } else if (abs >= 10000) {
    return `${(value / 10000).toFixed(2)}万`
  } else {
    return value.toLocaleString('zh-CN', { maximumFractionDigits: 2 })
  }
}

/**
 * 格式化日期
 */
export function formatDate(date: string | Date, format: 'short' | 'long' | 'time' = 'short'): string {
  try {
    const d = typeof date === 'string' ? new Date(date) : date
    if (isNaN(d.getTime())) return '-'
    if (format === 'short') {
      return `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, '0')}-${String(d.getDate()).padStart(2, '0')}`
    } else if (format === 'time') {
      return `${String(d.getHours()).padStart(2, '0')}:${String(d.getMinutes()).padStart(2, '0')}`
    } else {
      return `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, '0')}-${String(d.getDate()).padStart(2, '0')} ${String(d.getHours()).padStart(2, '0')}:${String(d.getMinutes()).padStart(2, '0')}`
    }
  } catch {
    return '-'
  }
}

/**
 * 格式化持仓权重
 */
export function formatWeight(weight: number): string {
  if (weight === null || weight === undefined || isNaN(weight)) return '-'
  return `${(weight * 100).toFixed(2)}%`
}

/**
 * 计算百分比
 */
export function calcPercent(value: number, total: number): number {
  if (total === 0) return 0
  return (value / total) * 100
}

/**
 * 简短名称（截断）
 */
export function truncateName(name: string, maxLen: number = 10): string {
  if (!name) return ''
  if (name.length <= maxLen) return name
  return name.slice(0, maxLen - 1) + '…'
}

/**
 * 生成颜色（用于图表）
 */
export function generateColors(count: number): string[] {
  const colors = [
    '#3B82F6', '#EF4444', '#10B981', '#F59E0B', '#8B5CF6',
    '#EC4899', '#06B6D4', '#84CC16', '#F97316', '#6366F1',
    '#14B8A6', '#E11D48', '#0EA5E9', '#A855F7', '#22C55E',
  ]
  return Array.from({ length: count }, (_, i) => colors[i % colors.length])
}
