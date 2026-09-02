import { useState, useEffect, useCallback } from 'react'
import {
  StartDailyWorkflow,
  GetTaskStatus,
  ListActiveTasks,
  GetAllAgentTasks,
  GetOrchestratorStats,
  ListWorkflowTemplates,
  RunManualWorkflow,
} from '../../wailsjs/go/main/App'
import { useToastStore } from '../store/toastStore'

type TaskStep = {
  id: string
  name: string
  assignee: string
  status: string
  output?: any
  error?: string
  started_at?: string
  ended_at?: string
}

type TaskProgress = {
  task_id: string
  name: string
  status: string
  progress: number
  completed: number
  total: number
  current_step: number
  steps: TaskStep[]
  created_at: string
  updated_at: string
}

type AgentTask = {
  id: string
  title: string
  type: string
  priority: string
}

type Template = {
  id: string
  name: string
  description: string
  steps: { id: string; name: string; assignee: string; required: boolean }[]
}

function WorkflowPanel() {
  const showError = useToastStore((s) => s.error)
  const showSuccess = useToastStore((s) => s.success)
  const showWarning = useToastStore((s) => s.warning)
  const [activeTasks, setActiveTasks] = useState<TaskProgress[]>([])
  const [agentTasks, setAgentTasks] = useState<Record<string, AgentTask[]>>({})
  const [stats, setStats] = useState<any>(null)
  const [templates, setTemplates] = useState<Template[]>([])
  const [selectedTask, setSelectedTask] = useState<TaskProgress | null>(null)
  const [loading, setLoading] = useState(true)

  const loadAll = useCallback(async () => {
    setLoading(true)
    try {
      const results = await Promise.allSettled([
        ListActiveTasks(),
        GetAllAgentTasks(),
        GetOrchestratorStats(),
        ListWorkflowTemplates(),
      ])

      const [tasksResult, agentTasksResult, statsResult, templatesResult] = results

      if (tasksResult.status === 'fulfilled') {
        setActiveTasks((tasksResult.value?.active_tasks as TaskProgress[]) || [])
      } else {
        showWarning('任务列表加载异常', '无法获取活跃任务列表')
      }

      if (agentTasksResult.status === 'fulfilled') {
        setAgentTasks((agentTasksResult.value as Record<string, AgentTask[]>) || {})
      }

      if (statsResult.status === 'fulfilled') {
        setStats(statsResult.value)
      } else {
        showWarning('编排器统计加载异常', '无法获取编排器状态')
      }

      if (templatesResult.status === 'fulfilled') {
        setTemplates((templatesResult.value?.templates as Template[]) || [])
      }
    } catch (err) {
      const errMsg = err instanceof Error ? err.message : String(err)
      showError('工作流数据加载失败', `加载过程出现异常: ${errMsg}`)
    } finally {
      setLoading(false)
    }
  }, [showError, showWarning])

  useEffect(() => {
    loadAll()
    const interval = setInterval(loadAll, 60000)
    return () => clearInterval(interval)
  }, [loadAll])

  const handleStartDailyWorkflow = async () => {
    try {
      const result = await StartDailyWorkflow()
      showSuccess('每日工作流已启动', `任务ID: ${result?.task_id || '未知'}`)
      loadAll()
    } catch (err) {
      const errMsg = err instanceof Error ? err.message : String(err)
      showError('启动失败', `每日工作流启动失败: ${errMsg}`)
    }
  }

  const handleRunTemplate = async (templateID: string) => {
    try {
      const result = await RunManualWorkflow(templateID)
      showSuccess('工作流已启动', result?.message || '工作流执行已启动')
      loadAll()
    } catch (err) {
      const errMsg = err instanceof Error ? err.message : String(err)
      showError('启动失败', `工作流启动失败: ${errMsg}`)
    }
  }

  const handleViewTask = async (taskID: string) => {
    try {
      const task = await GetTaskStatus(taskID) as TaskProgress
      setSelectedTask(task)
    } catch (err) {
      console.error('Failed to get task status:', err)
    }
  }

  const getStatusColor = (status: string) => {
    switch (status) {
      case 'COMPLETED': return 'text-green-600 bg-green-100 dark:text-green-400 dark:bg-green-900/30'
      case 'EXECUTING': return 'text-blue-600 bg-blue-100 dark:text-blue-400 dark:bg-blue-900/30'
      case 'PENDING': return 'text-yellow-600 bg-yellow-100 dark:text-yellow-400 dark:bg-yellow-900/30'
      case 'FAILED': return 'text-red-600 bg-red-100 dark:text-red-400 dark:bg-red-900/30'
      case 'APPROVED': return 'text-green-600 bg-green-100 dark:text-green-400 dark:bg-green-900/30'
      case 'REJECTED': return 'text-red-600 bg-red-100 dark:text-red-400 dark:bg-red-900/30'
      default: return 'text-slate-600 bg-slate-100 dark:text-slate-400 dark:bg-slate-700'
    }
  }

  const getPriorityColor = (priority: string) => {
    switch (priority) {
      case 'CRITICAL': return 'text-red-600 dark:text-red-400'
      case 'HIGH': return 'text-orange-600 dark:text-orange-400'
      case 'NORMAL': return 'text-blue-600 dark:text-blue-400'
      case 'LOW': return 'text-slate-600 dark:text-slate-400'
      default: return 'text-slate-600 dark:text-slate-400'
    }
  }

  const agentRoles = [
    { key: 'planner', name: 'Planner', color: 'bg-blue-500' },
    { key: 'quant', name: 'Quant', color: 'bg-purple-500' },
    { key: 'cio', name: 'CIO', color: 'bg-green-500' },
    { key: 'risk', name: 'Risk', color: 'bg-orange-500' },
    { key: 'trader', name: 'Trader', color: 'bg-pink-500' },
  ]

  return (
    <div className="space-y-4">
      {/* Header */}
      <div className="flex justify-between items-center">
        <div>
          <h1 className="text-xl font-bold text-slate-900 dark:text-slate-100">任务编排引擎</h1>
          <p className="text-sm text-slate-500 dark:text-slate-400">
            CIO 定时自动启动 · 各 Agent 自动监控执行 · 全流程无人干预
          </p>
        </div>
        <div className="flex gap-2">
          <button
            onClick={handleStartDailyWorkflow}
            className="px-3 py-1.5 bg-blue-600 dark:bg-blue-500 text-white text-sm rounded-lg hover:bg-blue-700 dark:hover:bg-blue-600 transition-colors"
          >
            手动启动每日工作流
          </button>
          <button
            onClick={loadAll}
            className="px-3 py-1.5 bg-slate-200 dark:bg-slate-700 text-slate-700 dark:text-slate-200 text-sm rounded-lg hover:bg-slate-300 dark:hover:bg-slate-600 transition-colors"
          >
            刷新
          </button>
        </div>
      </div>

      {/* Stats Cards */}
      {stats && (
        <div className="grid grid-cols-4 gap-3">
          <div className="bg-white dark:bg-slate-800 rounded-lg p-3 shadow dark:shadow-slate-900/20 border border-slate-200 dark:border-slate-700">
            <div className="text-xs text-slate-500 dark:text-slate-400">总任务数</div>
            <div className="text-xl font-bold text-slate-900 dark:text-slate-100">{stats.total_tasks || 0}</div>
          </div>
          <div className="bg-white dark:bg-slate-800 rounded-lg p-3 shadow dark:shadow-slate-900/20 border border-slate-200 dark:border-slate-700">
            <div className="text-xs text-slate-500 dark:text-slate-400">已完成任务</div>
            <div className="text-xl font-bold text-green-600 dark:text-green-400">{stats.task_status?.COMPLETED || 0}</div>
          </div>
          <div className="bg-white dark:bg-slate-800 rounded-lg p-3 shadow dark:shadow-slate-900/20 border border-slate-200 dark:border-slate-700">
            <div className="text-xs text-slate-500 dark:text-slate-400">执行中任务</div>
            <div className="text-xl font-bold text-blue-600 dark:text-blue-400">{stats.task_status?.EXECUTING || 0}</div>
          </div>
          <div className="bg-white dark:bg-slate-800 rounded-lg p-3 shadow dark:shadow-slate-900/20 border border-slate-200 dark:border-slate-700">
            <div className="text-xs text-slate-500 dark:text-slate-400">失败任务</div>
            <div className="text-xl font-bold text-red-600 dark:text-red-400">{stats.task_status?.FAILED || 0}</div>
          </div>
        </div>
      )}

      <div className="grid grid-cols-3 gap-4">
        {/* Left Column - Active Tasks */}
        <div className="col-span-2 space-y-4">
          {/* Active Tasks */}
          <div className="bg-white dark:bg-slate-800 rounded-lg shadow dark:shadow-slate-900/20 border border-slate-200 dark:border-slate-700">
            <div className="p-3 border-b border-slate-200 dark:border-slate-700">
              <h2 className="text-base font-semibold text-slate-900 dark:text-slate-100">活跃工作流</h2>
            </div>
            <div className="p-3">
              {loading ? (
                <div className="text-center text-slate-500 dark:text-slate-400 py-6">加载中...</div>
              ) : activeTasks.length === 0 ? (
                <div className="text-center text-slate-500 dark:text-slate-400 py-6">暂无活跃工作流</div>
              ) : (
                <div className="space-y-3">
                  {activeTasks.map((task) => (
                    <div
                      key={task.task_id}
                      className="border border-slate-200 dark:border-slate-700 rounded-lg p-3 hover:shadow-md dark:hover:shadow-slate-900/30 transition-shadow cursor-pointer"
                      onClick={() => handleViewTask(task.task_id)}
                    >
                      <div className="flex justify-between items-start mb-2">
                        <div>
                          <h3 className="font-medium text-slate-900 dark:text-slate-100">{task.name}</h3>
                          <p className="text-xs text-slate-500 dark:text-slate-400">{task.task_id}</p>
                        </div>
                        <span className={`px-2 py-1 text-xs rounded ${getStatusColor(task.status)}`}>
                          {task.status}
                        </span>
                      </div>
                      {/* Progress Bar */}
                      <div className="mb-2">
                        <div className="flex justify-between text-xs text-slate-500 dark:text-slate-400 mb-1">
                          <span>进度: {task.completed}/{task.total} 步骤</span>
                          <span>{task.progress != null ? task.progress.toFixed(0) : '0'}%</span>
                        </div>
                        <div className="w-full bg-slate-200 dark:bg-slate-700 rounded-full h-2">
                          <div
                            className="bg-blue-600 dark:bg-blue-500 h-2 rounded-full transition-all"
                            style={{ width: `${task.progress != null ? task.progress : 0}%` }}
                          />
                        </div>
                      </div>
                      {/* Steps Preview */}
                      <div className="flex gap-2 mt-2">
                        {task.steps?.map((step, idx) => (
                          <div
                            key={step.id}
                            className={`flex-1 h-1 rounded ${
                              step.status === 'COMPLETED' ? 'bg-green-500' :
                              step.status === 'APPROVED' ? 'bg-emerald-500' :
                              step.status === 'EXECUTING' ? 'bg-blue-500' :
                              step.status === 'PENDING' ? 'bg-yellow-500' :
                              step.status === 'FAILED' ? 'bg-red-500' :
                              step.status === 'REJECTED' ? 'bg-red-500' : 'bg-slate-300 dark:bg-slate-600'
                            }`}
                            title={`${idx + 1}. ${step.name} (${step.assignee})`}
                          />
                        ))}
                      </div>
                    </div>
                  ))}
                </div>
              )}
            </div>
          </div>

          {/* Task Detail Modal */}
          {selectedTask && (
            <div className="fixed inset-0 bg-black/50 flex items-center justify-center z-50" onClick={() => setSelectedTask(null)}>
              <div className="bg-white dark:bg-slate-800 rounded-lg w-full max-w-2xl max-h-[80vh] overflow-auto border border-slate-200 dark:border-slate-700" onClick={(e) => e.stopPropagation()}>
                <div className="p-4 border-b border-slate-200 dark:border-slate-700 flex justify-between items-center">
                  <h2 className="text-lg font-semibold text-slate-900 dark:text-slate-100">{selectedTask.name}</h2>
                  <button onClick={() => setSelectedTask(null)} className="text-slate-500 dark:text-slate-400 hover:text-slate-700 dark:hover:text-slate-200">
                    ✕
                  </button>
                </div>
                <div className="p-4">
                  <div className="mb-4">
                    <span className={`px-2 py-1 text-xs rounded ${getStatusColor(selectedTask.status)}`}>
                      {selectedTask.status}
                    </span>
                    <span className="ml-2 text-sm text-slate-500 dark:text-slate-400">
                      {selectedTask.progress != null ? selectedTask.progress.toFixed(0) : '0'}% 完成
                    </span>
                  </div>
                  <h3 className="font-medium mb-2 text-slate-900 dark:text-slate-100">执行步骤</h3>
                  <div className="space-y-2">
                    {selectedTask.steps?.map((step, idx) => (
                      <div
                        key={step.id}
                        className={`p-3 border rounded ${
                          step.status === 'COMPLETED' ? 'border-green-300 dark:border-green-800 bg-green-50 dark:bg-green-900/20' :
                          step.status === 'EXECUTING' ? 'border-blue-300 dark:border-blue-800 bg-blue-50 dark:bg-blue-900/20' :
                          step.status === 'FAILED' ? 'border-red-300 dark:border-red-800 bg-red-50 dark:bg-red-900/20' :
                          'border-slate-200 dark:border-slate-700'
                        }`}
                      >
                        <div className="flex justify-between items-center">
                          <div>
                            <span className="font-medium text-slate-900 dark:text-slate-100">{idx + 1}. {step.name}</span>
                            <span className="ml-2 text-xs text-slate-500 dark:text-slate-400">{step.assignee}</span>
                          </div>
                          <span className={`text-xs px-2 py-1 rounded ${getStatusColor(step.status)}`}>
                            {step.status}
                          </span>
                        </div>
                        {step.error && (
                          <p className="text-sm text-red-600 dark:text-red-400 mt-1">错误: {step.error}</p>
                        )}
                      </div>
                    ))}
                  </div>
                </div>
              </div>
            </div>
          )}

          {/* Workflow Templates */}
          <div className="bg-white dark:bg-slate-800 rounded-lg shadow dark:shadow-slate-900/20 border border-slate-200 dark:border-slate-700">
            <div className="p-3 border-b border-slate-200 dark:border-slate-700">
              <h2 className="text-base font-semibold text-slate-900 dark:text-slate-100">工作流模板</h2>
            </div>
            <div className="p-3">
              <div className="grid grid-cols-2 gap-3">
                {templates.map((tpl) => (
                  <div key={tpl.id} className="border border-slate-200 dark:border-slate-700 rounded-lg p-3 hover:shadow-md dark:hover:shadow-slate-900/30 transition-shadow">
                    <h3 className="font-medium text-slate-900 dark:text-slate-100 mb-1">{tpl.name}</h3>
                    <p className="text-xs text-slate-500 dark:text-slate-400 mb-2">{tpl.description}</p>
                    <div className="flex gap-1 mb-2 flex-wrap">
                      {tpl.steps?.map((step, idx) => (
                        <span key={idx} className="text-xs px-1 py-0.5 bg-slate-100 dark:bg-slate-700 text-slate-600 dark:text-slate-300 rounded">
                          {step.assignee}
                        </span>
                      ))}
                    </div>
                    <button
                      onClick={() => handleRunTemplate(tpl.id)}
                      className="w-full px-3 py-1 bg-blue-500 dark:bg-blue-600 text-white text-sm rounded hover:bg-blue-600 dark:hover:bg-blue-700 transition-colors"
                    >
                      启动
                    </button>
                  </div>
                ))}
              </div>
            </div>
          </div>
        </div>

        {/* Right Column - Agent Tasks */}
        <div className="space-y-4">
          {/* Agent Tasks */}
          <div className="bg-white dark:bg-slate-800 rounded-lg shadow dark:shadow-slate-900/20 border border-slate-200 dark:border-slate-700">
            <div className="p-3 border-b border-slate-200 dark:border-slate-700">
              <h2 className="text-base font-semibold text-slate-900 dark:text-slate-100">智能体待办</h2>
            </div>
            <div className="p-3">
              <div className="space-y-4">
                {agentRoles.map((role) => {
                  const tasks = agentTasks[role.key] as AgentTask[] | undefined
                  return (
                    <div key={role.key}>
                      <div className="flex items-center justify-between mb-2">
                        <div className="flex items-center gap-2">
                          <div className={`w-2 h-2 rounded-full ${role.color}`} />
                          <span className="font-medium text-sm text-slate-700 dark:text-slate-300">{role.name}</span>
                        </div>
                        <span className="text-xs text-slate-500 dark:text-slate-400">{tasks?.length || 0} 项待办</span>
                      </div>
                      {tasks && tasks.length > 0 ? (
                        <div className="space-y-1 ml-4">
                          {tasks.slice(0, 3).map((task) => (
                            <div key={task.id} className="text-xs p-2 bg-slate-50 dark:bg-slate-700/50 rounded">
                              <div className="flex justify-between">
                                <span className="text-slate-900 dark:text-slate-200">{task.title}</span>
                                <span className={getPriorityColor(task.priority)}>●</span>
                              </div>
                            </div>
                          ))}
                          {tasks.length > 3 && (
                            <div className="text-xs text-slate-400 dark:text-slate-500 text-center">
                              +{tasks.length - 3} 更多...
                            </div>
                          )}
                        </div>
                      ) : (
                        <div className="text-xs text-slate-400 dark:text-slate-500 ml-4">暂无待办</div>
                      )}
                    </div>
                  )
                })}
              </div>
            </div>
          </div>
        </div>
      </div>
    </div>
  )
}

export default function Workflow() {
  return <WorkflowPanel />
}
