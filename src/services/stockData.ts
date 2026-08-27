export interface StockData {
  code: string
  name: string
  market: 'sh' | 'sz'
  currentPrice: number
  prevClose: number
  open: number
  high: number
  low: number
  volume: number
  turnover: number
  changePercent: number
  changeAmount: number
  timestamp: number
  isMock: boolean
}

export interface MarketIndex {
  code: string
  name: string
  current: number
  change: number
  changePercent: number
  isMock: boolean
}

export interface WatchStock {
  code: string
  name: string
  market: string
  sector: string
}

export interface WatchIndex {
  code: string
  name: string
  market: string
  type: string
}

async function fetchViaBackend<T>(fnName: 'GetStockSnapshots' | 'GetIndexSnapshots' | 'GetWatchList', codes?: string[]): Promise<{ data: T[] | { stocks: WatchStock[]; indices: WatchIndex[] }; isMock: boolean } | null> {
  try {
    if (typeof window === 'undefined') {
      return null
    }
    const app = (window as any)?.go?.main?.App
    if (!app || typeof app[fnName] !== 'function') {
      return null
    }
    const result = codes ? await app[fnName](codes) : await app[fnName]()
    if (!result) return null
    if (Array.isArray(result.data)) {
      return { data: result.data as T[], isMock: !!result.isMock }
    }
    if (result.stocks && result.indices) {
      return { data: result as unknown as { stocks: WatchStock[]; indices: WatchIndex[] }, isMock: false }
    }
    return null
  } catch {
    return null
  }
}

// Get the real watch list from the backend database
export async function fetchWatchList(): Promise<{ stocks: WatchStock[]; indices: WatchIndex[] } | null> {
  try {
    const backendResult = await fetchViaBackend<WatchStock>('GetWatchList')
    if (backendResult && !backendResult.isMock) {
      return backendResult.data as unknown as { stocks: WatchStock[]; indices: WatchIndex[] }
    }
  } catch {}
  return null
}

export async function fetchStockData(codes?: string[]): Promise<{ data: StockData[]; isMock: boolean }> {
  let targetCodes: string[]

  if (codes && codes.length > 0) {
    targetCodes = codes
  } else {
    const watchList = await fetchWatchList()
    if (watchList && watchList.stocks.length > 0) {
      targetCodes = watchList.stocks.map((s) => `${s.market}${s.code}`)
    } else {
      return { data: [], isMock: false }
    }
  }

  // 统一走后端统一数据源接口（后端已按用户设置的数据源返回真实实时行情，
  // 内部自动回退腾讯财经；前端不再直连腾讯，避免 no-cors 失效 & 双端口径散落）
  const backendResult = await fetchViaBackend<StockData>('GetStockSnapshots', targetCodes)
  if (backendResult && Array.isArray(backendResult.data) && backendResult.data.length > 0) {
    return { data: backendResult.data as StockData[], isMock: !!backendResult.isMock }
  }

  // 无真实数据 — 返回空，严禁合成/伪造数据（页面按需自动重试）
  return { data: [], isMock: false }
}

export async function fetchIndexData(): Promise<{ data: MarketIndex[]; isMock: boolean }> {
  const watchList = await fetchWatchList()
  let targetCodes: string[]

  if (watchList && watchList.indices.length > 0) {
    // index.code 已含交易所前缀（如 sh000001），避免拼出 market+code 双重前缀（如 SHsh000001）
    targetCodes = watchList.indices.map((i) => {
      const c = (i.code || '').trim().toLowerCase()
      const m = (i.market || '').trim().toLowerCase()
      if (/^(sh|sz|bj)/.test(c)) return c
      return c.length === 6 ? `${m}${c}` : c
    }).filter(Boolean)
  } else {
    return { data: [], isMock: false }
  }

  // 统一走后端统一数据源接口
  const backendResult = await fetchViaBackend<MarketIndex>('GetIndexSnapshots', targetCodes)
  if (backendResult && Array.isArray(backendResult.data) && backendResult.data.length > 0) {
    return { data: backendResult.data as MarketIndex[], isMock: !!backendResult.isMock }
  }

  // 无实时数据（后端按当前数据源 + 腾讯财经回退均取不到）——绝不回退离线库或伪造数据
  throw new Error('未获取到实时指数行情数据，请检查通达信/数据源连接（数据必须为实时行情）')
}
