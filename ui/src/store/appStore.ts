import { create } from 'zustand'
import { RunDailyCycle } from '../../wailsjs/go/main/App'

interface DailyCycleResult {
  Date?: string
  EndTime?: string
  Errors?: string[]
  MarketView?: any
  AlphaSignals?: any
  RiskAssessment?: any
  PortfolioPlan?: any
  Decision?: any
  Executed?: boolean
  [key: string]: any
}

interface AppState {
  isFirstRun: boolean
  dailyReport: DailyCycleResult | null
  isLoading: boolean
  error: string | null
  isAutoRefresh: boolean
  fetchDailyReport: () => Promise<void>
  setDailyReport: (report: DailyCycleResult | null) => void
  setIsLoading: (loading: boolean) => void
  setFirstRun: (value: boolean) => void
}

export const useAppStore = create<AppState>((set) => ({
  isFirstRun: false,
  dailyReport: null,
  isLoading: false,
  error: null,
  isAutoRefresh: true,
  setFirstRun: (value) => set({ isFirstRun: value }),
  setDailyReport: (report: DailyCycleResult | null) => set({ dailyReport: report }),
  setIsLoading: (loading: boolean) => set({ isLoading: loading }),
  fetchDailyReport: async () => {
    set({ isLoading: true, error: null })
    try {
      await RunDailyCycle()
    } catch (err: any) {
      set({ error: err?.message || String(err), isLoading: false })
      throw err
    }
  },
}))
