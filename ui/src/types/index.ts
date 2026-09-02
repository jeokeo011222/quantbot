export interface DailyCycleResult {
  Date: string
  StartTime: string
  EndTime: string
  MarketView: any
  AlphaSignals: any
  RiskAssessment: any
  PortfolioPlan: any
  Decision: any
  Executed: boolean
  Errors: string[]
}

export interface AgentSession {
  ID: string
  StartTime: string
  EndTime: string
  Confidence: number
  Decision: any
  DecisionType: string
  ToolCalls: any[]
  Steps: any[]
  Status: string
}

export interface AppConfig {
  market: string
  trading_mode: string
  ai_provider: string
  ai_model: string
  riskos_enabled: boolean
  broker_type: string
  data_provider: string
  initial_capital: number
  currency: string
  auto_daily_run: boolean
  daily_run_time: string
}

// ==================== Investment Planner Types ====================

export type PlannerStep = 'WELCOME' | 'INTERVIEW' | 'PLANNING' | 'REVIEW' | 'COMPLETE'

export interface InvestorProfile {
  id: number
  capital: number
  currency: string
  investmentExperience: string
  investmentStyle: string
  investmentHorizon: string
  investmentObjective: string
  riskTolerance: string
  drawdownTolerance: number
  lossTolerance: number
  liquidityRequirement: string
  tradingFrequency: string
  marketPreference: string
  profileCompleteness: number
  currentStep: PlannerStep
  profile: Record<string, any>
}

export interface ConversationMessage {
  id: number
  role: 'user' | 'assistant' | 'system'
  content: string
  messageType: string
  createdAt: string
}

export interface Conversation {
  id: number
  conversationId: string
  title: string
  status: string
  currentStep: string
  messageCount: number
  messages: ConversationMessage[]
}

export interface CandidateStrategy {
  id: number
  candidateId: string
  name: string
  label: 'A' | 'B' | 'C'
  description: string
  expectedReturn: number
  expectedVolatility: number
  maxDrawdown: number
  sharpe: number
  calmar: number
  robustnessScore: number
  riskScore: number
  riskOsStatus: string
  status: string
}

export interface InvestmentPlan {
  id: number
  planId: string
  name: string
  objective: string
  riskLevel: string
  targetReturn: number
  targetVolatility: number
  maxDrawdown: number
  strategyType: string
  status: string
  mandate?: InvestmentMandate
  construction?: PortfolioConstruction
  candidates: CandidateStrategy[]
  stockPool?: PlanStockPool | null
  createdAt: string
}

// 投资方案列表摘要（我的投资方案）
export interface PlanSummary {
  id: number
  planId: string
  name: string
  objective: string
  riskLevel: string
  targetReturn: number
  targetVolatility: number
  maxDrawdown: number
  strategyType: string
  status: string
  stockPool?: PlanStockPool | null
  createdAt: string
}

// ============ 投资规划股票池 ============

export interface PlanStockPool {
  strategyId: string
  submittedCount: number
  totalResults: number
  generatedAt: string
  profileSummary?: string
}

// ============ Investment Mandate ============

export interface InvestmentMandate {
  mandateId: string
  title: string
  version: number
  investorProfile: Record<string, string>
  investmentObjective: string
  riskLevel: string
  targetReturnRange: [number, number]
  targetVolatility: number
  maxDrawdown: number
  totalRiskBudget: number
  positionLimit: {
    maxSingleStock: number
    maxIndustry: Record<string, number>
    maxFactorExposure: Record<string, number>
    highVolLimit: number
  }
  cashRequirement: [number, number]
  leverageLimit: number
  marketUniverse: string[]
  allowedIndustries: string[]
  restrictedIndustries: string[]
  stylePreferences: {
    primaryStyle: string
    factorWeights: Record<string, number>
    preferredIndustries: string[]
  }
  rebalancePolicy: {
    frequency: string
    maxTurnover: number
    driftThreshold: number
  }
  generatedAt: string
  generatedBy: string
}

// ============ Portfolio Construction ============

export interface PortfolioConstruction {
  processSteps: ConstructionStep[]
  positions: PortfolioPosition[]
  totalAssets: number
  equityWeight: number
  fundWeight: number
  bondWeight: number
  cashWeight: number
  industryDist: Record<string, number>
  expectedReturn: number
  expectedVolatility: number
  maxDrawdown: number
  sharpeRatio: number
  status: string
  generatedAt: string
}

export interface ConstructionStep {
  stepNo: number
  title: string
  agentRole: string
  status: 'PENDING' | 'RUNNING' | 'DONE' | 'FAILED'
  message: string
  detail: string
}

export interface PortfolioPosition {
  assetCode: string
  assetName: string
  assetType: 'STOCK' | 'ETF' | 'BOND' | 'CASH'
  industry: string
  targetWeight: number
  suggestedAmount: number
  baseWeight: number
  alphaAdjust: number
  riskAdjust: number
  factorScore: number
  riskOsStatus: string
  rationale: string
  quantEvidence: Record<string, number>
  stockPrice?: number
  lotSize?: number
  minTradeCost?: number
  lots?: number
  tradeable?: boolean
}

export interface InterviewResponse {
  message: string
  isComplete: boolean
  nextQuestion?: string
  profileUpdate?: Record<string, any>
  suggestions?: string[]
}

export interface PlannerState {
  step: PlannerStep
  progress: number
  profileCompleteness: number
  hasActivePlan: boolean
}

// ==================== AI Investment Team Types ====================

export type AgentRole = 'CIO' | 'PLANNER' | 'QUANT' | 'RISK' | 'TRADER'

export type AgentState = 
  | 'IDLE' | 'THINKING' | 'TOOL_CALLING' | 'WAITING' 
  | 'PROPOSING' | 'REVIEWING' | 'COMPLETED' | 'FAILED'
  | 'BLOCKED' | 'SLEEPING'
  | 'APPROVED' | 'ORDER_CREATED' | 'SUBMITTED' | 'PARTIAL_FILLED' 
  | 'FILLED' | 'VERIFIED'

export interface AgentStatusInfo {
  id: string
  role: AgentRole
  name: string
  state: AgentState
}

export interface CIODecisionInfo {
  decision: string
  reason: string
  decision_id: string
  risk_approval: string
  policy_status: string
  timestamp: string
}

export interface CIODecisionLogEntry {
  decision_id: string
  decision: string
  reason: string
  risk_approval: string
  policy_status: string
  timestamp: string
}

export interface TeamActivity {
  agent_role: AgentRole
  activity_type: string
  title: string
  message: string
  timestamp: string
}

export interface CIOStatus {
  cio: AgentStatusInfo
  quant: AgentStatusInfo
  risk: AgentStatusInfo
  trader: AgentStatusInfo
  state: 'NORMAL' | 'REVIEWING' | 'ACTION_REQUIRED'
  emergency: boolean
}

export interface PolicyStatus {
  emergency_stop: boolean
  hard_limits: HardLimits
}

export interface HardLimits {
  MaxSinglePosition: number
  MaxSectorExposure: number
  MaxPortfolioLeverage: number
  MaxDailyLoss: number
  MaxDrawdown: number
  MaxOrderSize: number
  MaxTurnover: number
  MaxSlippage: number
  MinLiquidity: number
}

export const AGENT_ROLE_NAMES: Record<AgentRole, string> = {
  CIO: '首席投资官',
  PLANNER: '投资规划师',
  QUANT: '量化分析师',
  RISK: '风控师',
  TRADER: '操盘手',
}

export const AGENT_ROLE_DESCRIPTIONS: Record<AgentRole, { title: string; duty: string; canDo: string[]; cannotDo: string[] }> = {
  CIO: {
    title: '首席投资官 / AI基金经理',
    duty: '最终投资决策',
    canDo: ['决定买什么/卖什么', '决定买卖多少', '审批交易决策', '主持复盘会议', '制定投资策略'],
    cannotDo: ['绕过风控直接交易', '直接执行交易', '修改风控参数'],
  },
  PLANNER: {
    title: '投资规划师',
    duty: '理解用户，形成 Investment Mandate',
    canDo: ['生成投资规划', '检查用户目标变化', '生成 Investment Mandate', 'Mandate 合规检查'],
    cannotDo: ['选股', '下单', '修改风控参数'],
  },
  QUANT: {
    title: '量化分析师',
    duty: '发现机会，提供候选方案',
    canDo: ['因子分析', '股票评分', 'Alpha 信号挖掘', '因子健康度监测', '生成候选池'],
    cannotDo: ['最终决定买卖', '直接下单', '修改投资约束'],
  },
  RISK: {
    title: '风控师',
    duty: '控制风险，独立审查，拥有否决权',
    canDo: ['风险评估', '设置风险限额', '否决交易', '紧急熔断', '生成风险报告'],
    cannotDo: ['主动选股', '改变投资意图', '绕过 CIO 决策'],
  },
  TRADER: {
    title: '操盘手',
    duty: '执行交易，按指令拆单执行',
    canDo: ['拆单执行 (VWAP/TWAP)', '成交管理', '滑点优化', '交易成本控制', '生成执行报告'],
    cannotDo: ['改变投资意图', '修改目标仓位', '绕过 CIO 决策'],
  },
}

export const AGENT_PIPELINE: { step: number; role: AgentRole; action: string; permission: string }[] = [
  { step: 1, role: 'PLANNER', action: '提出方案', permission: 'PROPOSE' },
  { step: 2, role: 'QUANT', action: '提供证据', permission: 'EVIDENCE' },
  { step: 3, role: 'RISK', action: '风险否决', permission: 'VETO' },
  { step: 4, role: 'CIO', action: '最终决策', permission: 'DECIDE' },
  { step: 5, role: 'TRADER', action: '执行交易', permission: 'EXECUTE' },
]

export const AGENT_STATE_LABELS: Record<AgentState, string> = {
  IDLE: '待命',
  THINKING: '思考中',
  TOOL_CALLING: '调用工具',
  WAITING: '等待中',
  PROPOSING: '提出方案',
  REVIEWING: '审查中',
  COMPLETED: '完成',
  FAILED: '异常',
  BLOCKED: '已阻断',
  SLEEPING: '休眠中',
  APPROVED: '已批准',
  ORDER_CREATED: '订单已创建',
  SUBMITTED: '已提交',
  PARTIAL_FILLED: '部分成交',
  FILLED: '已成交',
  VERIFIED: '已验证',
}

// ==================== Agent 模式 ====================

export type AgentMode = 'STANDARD' | 'PTC' | 'MINIMAL' | 'CREATOR'

export const AGENT_MODE_LABELS: Record<AgentMode, string> = {
  STANDARD: '标准ReAct',
  PTC: 'Plan-Tool-Critique',
  MINIMAL: '最小模式（纯LLM）',
  CREATOR: '创意模式',
}

// ==================== Skill 技能类型 ====================

export interface SkillKnowledge {
  type: 'prompt' | 'template' | 'reference' | 'rule'
  content: string
  path?: string
  priority: number
}

export interface Skill {
  id: string
  name: string
  description: string
  version: string
  author: string
  tags: string[]
  system_prompt: string
  knowledge: SkillKnowledge[]
  tool_names: string[]
  enabled: boolean
  created_at: string
}

// ==================== Trajectory 轨迹类型 ====================

export type StepType = 'THINK' | 'TOOL_CALL' | 'TOOL_RESULT' | 'SUB_AGENT' | 'CRITIQUE' | 'FINAL_ANSWER' | 'ERROR'

export interface TrajectoryStep {
  step: number
  timestamp: string
  type: StepType
  thought?: string
  action?: string
  action_input?: any
  observation?: any
  result?: string
  duration_ms: number
  token_delta: number
}

export interface TokenUsage {
  prompt_tokens: number
  completion_tokens: number
  total_tokens: number
  estimated_cost: number
}

export interface Trajectory {
  id: string
  agent_id: string
  agent_name: string
  agent_role: AgentRole
  skill_id?: string
  mode: AgentMode
  start_time: string
  end_time: string
  status: string
  input: string
  output: string
  steps: TrajectoryStep[]
  sub_agents: string[]
  parent_id?: string
  confidence: number
  token_usage: TokenUsage
  metadata?: Record<string, any>
}

// ==================== Workflow 工作流类型 ====================

export type WorkflowStepType = 'tool_call' | 'sub_agent' | 'llm_reason' | 'condition' | 'transform'

export interface WorkflowStepDef {
  id: string
  name: string
  type: WorkflowStepType
  tool_name?: string
  tool_args?: Record<string, any>
  sub_agent_id?: string
  prompt?: string
  condition?: string
  transform?: string
  max_retries?: number
  timeout_ms?: number
}

export interface WorkflowDef {
  id: string
  name: string
  description: string
  steps: WorkflowStepDef[]
  on_failure: 'stop' | 'skip' | 'retry'
}

// ==================== Tool 注册表类型 ====================

export type ToolCategory = 'market_data' | 'stock_search' | 'market_stats' | 'backtest' | 'risk' | 'execution' | 'portfolio' | 'research' | 'system'

export interface ToolMetadata {
  name: string
  description: string
  category: ToolCategory
  roles: AgentRole[]
  risk_level: 'low' | 'medium' | 'high'
  enabled: boolean
  version: string
}

export const TOOL_CATEGORY_LABELS: Record<ToolCategory, string> = {
  market_data: '市场数据',
  stock_search: '股票搜索',
  market_stats: '市场统计',
  backtest: '策略回测',
  risk: '风险管理',
  execution: '交易执行',
  portfolio: '组合管理',
  research: '研究分析',
  system: '系统工具',
}

// ==================== Orchestrator 结果类型 ====================

export interface OrchestratorResult {
  date: string
  start_time: string
  end_time: string
  trajectories: Record<string, Trajectory>
  sessions: Record<string, AgentSession>
  decision: any
  errors: string[]
  total_tokens: number
}

// ==================== DailyCycleResult 扩展 ====================

export interface DailyCycleResultV2 extends DailyCycleResult {
  trajectories?: Record<string, Trajectory>
  token_usage?: TokenUsage
  use_orchestrator: boolean
}