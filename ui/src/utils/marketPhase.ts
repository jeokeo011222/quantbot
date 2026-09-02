// 市场时段工具：与后端 internal/util/trading_hours.go、trading_calendar.go 保持一致
export type MarketPhase =
  | 'PRE_MARKET'
  | 'PRE_OPEN'
  | 'MORNING_SESSION'
  | 'LUNCH'
  | 'AFTERNOON_SESSION'
  | 'POST_MARKET'
  | 'REVIEW'
  | 'CLOSED'

// A股法定节假日休市日历（仅记录工作日休市日，周末自动休市）
// 与后端 internal/util/trading_calendar.go 的 cnHolidays 保持一致
const cnHolidays = new Set<string>([
  // 2025年
  '2025-01-01', // 元旦
  '2025-01-28', '2025-01-29', '2025-01-30', '2025-01-31', '2025-02-03', '2025-02-04', // 春节
  '2025-04-04', // 清明节
  '2025-05-01', '2025-05-02', '2025-05-05', // 劳动节
  '2025-06-02', // 端午节
  '2025-10-01', '2025-10-02', '2025-10-03', '2025-10-06', '2025-10-07', '2025-10-08', // 国庆+中秋

  // 2026年
  '2026-01-01', '2026-01-02', // 元旦
  '2026-02-16', '2026-02-17', '2026-02-18', '2026-02-19', '2026-02-20', '2026-02-23', // 春节
  '2026-04-06', // 清明节
  '2026-05-01', '2026-05-04', '2026-05-05', // 劳动节
  '2026-06-19', // 端午节
  '2026-09-25', // 中秋节
  '2026-10-01', '2026-10-02', '2026-10-05', '2026-10-06', '2026-10-07', // 国庆节
])

// 获取当前市场时段
export const getCurrentPhase = (now: Date = new Date()): MarketPhase => {
  const day = now.getDay()
  const timeInMinutes = now.getHours() * 60 + now.getMinutes()
  const dateStr = `${now.getFullYear()}-${String(now.getMonth() + 1).padStart(2, '0')}-${String(now.getDate()).padStart(2, '0')}`

  // 周末或法定节假日休市
  if (day === 0 || day === 6 || cnHolidays.has(dateStr)) return 'CLOSED'

  // 交易日盘前准备 (开盘前-9:15)
  if (timeInMinutes < 9 * 60 + 15) return 'PRE_MARKET'
  // 盘前集合竞价 (9:15-9:30)
  if (timeInMinutes >= 9 * 60 + 15 && timeInMinutes < 9 * 60 + 30) return 'PRE_OPEN'
  // 上午交易时段 (9:30-11:30)
  if (timeInMinutes >= 9 * 60 + 30 && timeInMinutes < 11 * 60 + 30) return 'MORNING_SESSION'
  // 午休 (11:30-13:00)
  if (timeInMinutes >= 11 * 60 + 30 && timeInMinutes < 13 * 60) return 'LUNCH'
  // 下午交易时段 (13:00-15:00)
  if (timeInMinutes >= 13 * 60 && timeInMinutes < 15 * 60) return 'AFTERNOON_SESSION'
  // 盘后 (15:00-15:15)
  if (timeInMinutes >= 15 * 60 && timeInMinutes < 15 * 60 + 15) return 'POST_MARKET'
  // 复盘 (15:15-18:00)
  if (timeInMinutes >= 15 * 60 + 15 && timeInMinutes < 18 * 60) return 'REVIEW'
  return 'CLOSED'
}

// 时段中文标签
export const phaseLabels: Record<MarketPhase, string> = {
  PRE_MARKET: '盘前准备 (开盘前)',
  PRE_OPEN: '盘前集合竞价 (9:15-9:30)',
  MORNING_SESSION: '上午交易时段 (9:30-11:30)',
  LUNCH: '午休 (11:30-13:00)',
  AFTERNOON_SESSION: '下午交易时段 (13:00-15:00)',
  POST_MARKET: '盘后 (15:00-15:15)',
  REVIEW: '复盘 (15:15-18:00)',
  CLOSED: '休市中',
}
