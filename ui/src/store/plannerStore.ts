import { create } from 'zustand'
import type {
  PlannerStep,
  PlannerState,
  InvestorProfile,
  InvestmentPlan,
  CandidateStrategy,
  PlanSummary,
} from '../types'
import {
  GetPlannerState,
  GetProfile,
  SaveProfileAnswers,
  GeneratePlan,
  GeneratePlanStockPool,
  GetGeneratePlanProgress,
  StartGeneratePlan,
  GetCurrentPlan,
  GetPlans,
  GetPlan,
  ApprovePlan,
  ResetPlanner,
  CIOReviewPlan,
} from '../../wailsjs/go/main/App'
import { useToastStore } from './toastStore'

// 投资方案列表加载失败时的自动重试定时器
let listPlansRetryTimer: ReturnType<typeof setTimeout> | null = null

interface PlannerStore {
  state: PlannerState
  profile: InvestorProfile | null
  plan: InvestmentPlan | null
  candidates: CandidateStrategy[]
  plans: PlanSummary[]
  progress: string
  isLoading: boolean
  loadingPlans: boolean
  error: string | null
  currentStep: PlannerStep

  initPlanner: () => Promise<void>
  setProfileAnswers: (answers: Record<string, string>) => Promise<void>
  generatePlan: () => Promise<void>
  generateStockPool: () => Promise<void>
  loadPlan: () => Promise<void>
  listPlans: () => Promise<void>
  getPlanDetail: (planId: string) => Promise<InvestmentPlan | null>
  approvePlan: (planId: string, acknowledged: boolean) => Promise<void>
  resetPlanner: () => Promise<void>
  setStep: (step: PlannerStep) => void
}

const defaultState: PlannerState = {
  step: 'WELCOME',
  progress: 0,
  profileCompleteness: 0,
  hasActivePlan: false,
}

export const usePlannerStore = create<PlannerStore>((set) => ({
  state: defaultState,
  profile: null,
  plan: null,
  candidates: [],
  plans: [],
  progress: '',
  isLoading: false,
  loadingPlans: false,
  error: null,
  currentStep: 'WELCOME',

  initPlanner: async () => {
    set({ isLoading: true, error: null })
    try {
      const [state, profile] = await Promise.all([
        GetPlannerState() as unknown as PlannerState,
        GetProfile() as unknown as InvestorProfile,
      ])

      set({
        state,
        profile,
        currentStep: state.step,
      })

      if (state.hasActivePlan) {
        try {
          const plan = await GetCurrentPlan() as unknown as InvestmentPlan
          if (plan) {
            set({
              plan,
              candidates: plan.candidates || [],
              currentStep: 'REVIEW',
            })
          }
        } catch {
          // ignore
        }
      }
    } catch (err: any) {
      set({ error: err?.message || 'Failed to initialize planner' })
    } finally {
      set({ isLoading: false })
    }
  },

  setProfileAnswers: async (answers: Record<string, string>) => {
    try {
      await SaveProfileAnswers(answers)
      const profile = await GetProfile() as unknown as InvestorProfile
      const state = await GetPlannerState() as unknown as PlannerState

      set({
        profile,
        state,
      })
    } catch (err: any) {
      console.error('Failed to save profile answers:', err)
    }
  },

  generatePlan: async () => {
    let reviewDone = false
    const completePlan = async (plan: InvestmentPlan) => {
      if (reviewDone) return
      set({ plan, candidates: plan.candidates || [] })
      const state = await GetPlannerState() as unknown as PlannerState
      set({ state })
      // 自动触发 CIO 审核投资方案
      if (plan?.planId) {
        try {
          const reviewResult = await CIOReviewPlan(plan.planId) as unknown as { approved: boolean; reason: string }
          if (reviewResult?.approved) {
            const updatedPlan = await GetPlan(plan.planId) as unknown as InvestmentPlan
            set({
              plan: updatedPlan,
              candidates: updatedPlan?.candidates || plan.candidates || [],
              currentStep: 'COMPLETE',
            })
          } else {
            set({ currentStep: 'REVIEW' })
          }
        } catch (reviewErr) {
          console.error('CIO review failed:', reviewErr)
          set({ currentStep: 'REVIEW' })
        }
      } else {
        set({ currentStep: 'REVIEW' })
      }
      reviewDone = true
      set({ isLoading: false })
    }

    set({ isLoading: true, error: null, currentStep: 'PLANNING', progress: '' })
    try {
      // 启动异步生成任务
      const startResult = await StartGeneratePlan() as unknown as { running: boolean; progress: string }
      set({ progress: startResult?.progress || '正在启动投资方案生成…' })

      // 轮询进度，直到任务完成
      const poll = async () => {
        try {
          const progressResult = await GetGeneratePlanProgress() as unknown as {
            running: boolean
            done: boolean
            progress: string
            error?: string
            result?: InvestmentPlan
          }
          if (progressResult.progress) set({ progress: progressResult.progress })

          if (progressResult.done) {
            if (progressResult.error) {
              set({ error: progressResult.error, currentStep: 'INTERVIEW', isLoading: false })
              return
            }
            if (progressResult.result) {
              await completePlan(progressResult.result)
              return
            }
            // 无结果但已完成——回退同步查询
            console.warn('异步生成完成但无结果，回退同步查询')
            const plan = await GeneratePlan() as unknown as InvestmentPlan
            await completePlan(plan)
            return
          }
          // 继续轮询
          setTimeout(poll, 1200)
        } catch (err: any) {
          console.error('轮询进度失败:', err)
          setTimeout(poll, 1200)
        }
      }
      poll()
    } catch (err: any) {
      set({ error: err?.message || 'Failed to generate plan', currentStep: 'INTERVIEW', isLoading: false })
    }
  },

  generateStockPool: async () => {
    set({ isLoading: true, error: null })
    try {
      const plan = await GeneratePlanStockPool() as unknown as InvestmentPlan
      set({
        plan,
        candidates: plan.candidates || [],
      })
    } catch (err: any) {
      set({ error: err?.message || 'Failed to generate stock pool' })
    } finally {
      set({ isLoading: false })
    }
  },

  loadPlan: async () => {
    set({ isLoading: true })
    try {
      const plan = await GetCurrentPlan() as unknown as InvestmentPlan
      if (plan) {
        set({
          plan,
          candidates: plan.candidates || [],
        })
      }
    } catch (err: any) {
      set({ error: err?.message || 'Failed to load plan' })
    } finally {
      set({ isLoading: false })
    }
  },

  listPlans: async () => {
    set({ loadingPlans: true, error: null })
    try {
      const plans = await GetPlans() as unknown as PlanSummary[]
      set({ plans: plans || [] })
    } catch (err: any) {
      set({ error: err?.message || 'Failed to load plans' })
      // 数据获取失败：弹窗警示 + 30 秒自动重试（首次失败弹一次）
      useToastStore.getState().error(
        '投资方案列表加载失败，将在30秒后自动重试',
        err?.message || String(err)
      )
      if (listPlansRetryTimer) clearTimeout(listPlansRetryTimer)
      listPlansRetryTimer = setTimeout(() => {
        usePlannerStore.getState().listPlans()
      }, 30000)
    } finally {
      set({ loadingPlans: false })
    }
  },

  getPlanDetail: async (planId: string) => {
    try {
      const detail = await GetPlan(planId) as unknown as InvestmentPlan
      return detail
    } catch (err: any) {
      set({ error: err?.message || 'Failed to load plan detail' })
      return null
    }
  },

  approvePlan: async (planId: string, acknowledged: boolean) => {
    set({ isLoading: true, error: null })
    try {
      await ApprovePlan(planId, acknowledged)
      set({ currentStep: 'COMPLETE' })
    } catch (err: any) {
      set({ error: err?.message || 'Failed to approve plan' })
    } finally {
      set({ isLoading: false })
    }
  },

  resetPlanner: async () => {
    set({ isLoading: true, error: null })
    try {
      await ResetPlanner()
      set({
        state: defaultState,
        profile: null,
        plan: null,
        candidates: [],
        currentStep: 'WELCOME',
      })
    } catch (err: any) {
      set({ error: err?.message || 'Failed to reset planner' })
    } finally {
      set({ isLoading: false })
    }
  },

  setStep: (step: PlannerStep) => {
    set({ currentStep: step })
  },
}))
