import { useState, useCallback, useEffect } from 'react'

// 自选股管理 Hook - 使用 LocalStorage 存储
export interface WatchItem {
  code: string
  name: string
  addedAt: number
}

const STORAGE_KEY = 'quantpilot_watchlist'

function loadWatchlist(): WatchItem[] {
  try {
    const raw = localStorage.getItem(STORAGE_KEY)
    if (raw) {
      return JSON.parse(raw)
    }
  } catch {
    // ignore
  }
  return []
}

function saveWatchlist(items: WatchItem[]) {
  try {
    localStorage.setItem(STORAGE_KEY, JSON.stringify(items))
  } catch {
    // ignore
  }
}

export function useWatchlist() {
  const [watchlist, setWatchlist] = useState<WatchItem[]>(() => loadWatchlist())

  useEffect(() => {
    saveWatchlist(watchlist)
  }, [watchlist])

  const isInWatchlist = useCallback(
    (code: string) => watchlist.some((item) => item.code === code),
    [watchlist]
  )

  const addToWatchlist = useCallback((code: string, name: string) => {
    setWatchlist((prev) => {
      if (prev.some((item) => item.code === code)) return prev
      return [...prev, { code, name, addedAt: Date.now() }]
    })
  }, [])

  const removeFromWatchlist = useCallback((code: string) => {
    setWatchlist((prev) => prev.filter((item) => item.code !== code))
  }, [])

  const toggleWatchlist = useCallback((code: string, name: string) => {
    if (isInWatchlist(code)) {
      removeFromWatchlist(code)
    } else {
      addToWatchlist(code, name)
    }
  }, [isInWatchlist, removeFromWatchlist, addToWatchlist])

  const clearWatchlist = useCallback(() => {
    setWatchlist([])
  }, [])

  return {
    watchlist,
    isInWatchlist,
    addToWatchlist,
    removeFromWatchlist,
    toggleWatchlist,
    clearWatchlist,
  }
}
