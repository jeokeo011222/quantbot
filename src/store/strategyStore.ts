import { create } from 'zustand'
import { persist } from 'zustand/middleware'

export interface Strategy {
  id: string
  name: string
  status: 'running' | 'stopped'
  sharpe: number
  maxDD: number
  ret: number
  description?: string
  type?: string
}

interface StrategyStore {
  strategies: Strategy[]
  addStrategy: (strategy: Omit<Strategy, 'id'>) => void
  deleteStrategy: (id: string) => void
  toggleStatus: (id: string) => void
  getStrategyById: (id: string) => Strategy | undefined
}

// 严禁使用静态示例策略数据，策略列表必须通过API从后端真实获取
// 初始化为空数组，由组件挂载时从后端加载真实策略
const defaultStrategies: Strategy[] = []

export const useStrategyStore = create<StrategyStore>()(
  persist(
    (set, get) => ({
      strategies: defaultStrategies,

      addStrategy: (strategy) => {
        const newId = Date.now().toString()
        set((state) => ({
          strategies: [...state.strategies, { ...strategy, id: newId }]
        }))
      },

      deleteStrategy: (id) => {
        set((state) => ({
          strategies: state.strategies.filter(item => item.id !== id)
        }))
      },

      toggleStatus: (id) => {
        set((state) => ({
          strategies: state.strategies.map(item =>
            item.id === id
              ? { ...item, status: item.status === 'running' ? 'stopped' : 'running' }
              : item
          )
        }))
      },

      getStrategyById: (id) => {
        return get().strategies.find(item => item.id === id)
      },
    }),
    {
      name: 'quantpilot-strategies',
    }
  )
)
