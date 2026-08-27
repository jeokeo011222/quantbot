import { useState, useEffect, useRef, type ReactNode } from 'react'
import { Sparkles, RotateCcw, CheckCircle, ChevronRight, Wallet, Clock, Target, Shield, Droplets, BarChart3, TrendingUp, Building2, User, PieChart, LineChart, AlertTriangle, FileText, Activity, FolderOpen, X, ArrowLeft, Calendar, Layers } from 'lucide-react'
import { usePlannerStore } from '../store/plannerStore'
import type { CandidateStrategy, PlanSummary, InvestmentPlan } from '../types'

// 选择题数据结构
interface ChoiceOption {
  value: string
  label: string
  description?: string
  icon?: string
}

interface ChoiceQuestion {
  key: string
  title: string
  subtitle: string
  icon: ReactNode
  options: ChoiceOption[]
  multiSelect?: boolean
}

// 选择题配置（参考国内《私募投资基金募集行为管理办法》与私募证券基金管理人风险测评问卷）
// 本系统为股票型私募基金公司，全部以 A 股权益（股票多头/量化多头/指数增强）为投资方向，
// 不含债券、货币、混合等固收类资产；所有问题的选项均围绕股票型私募策略设计。
const CHOICE_QUESTIONS: ChoiceQuestion[] = [
  {
    key: 'investment_experience',
    title: '证券投资经验',
    subtitle: '请如实选择您的证券投资经验（单选）',
    icon: <User className="w-6 h-6" />,
    options: [
      { value: 'NOVICE', label: '新手', description: '证券投资经验不足1年，未接触过股票或股票型基金/私募' },
      { value: 'INTERMEDIATE', label: '有一定经验', description: '投资经验1-3年，了解A股、股票型公募等权益类产品' },
      { value: 'EXPERIENCED', label: '经验丰富', description: '投资经验3-5年，熟悉股票多头、量化多头等策略运作' },
      { value: 'SENIOR', label: '资深投资者', description: '投资经验5年以上，具备专业投资知识和丰富实战经验（含私募证券投资基金）' },
    ],
  },
  {
    key: 'product_experience',
    title: '股票型私募产品经验',
    subtitle: '您实际投资过哪些股票类/私募证券类产品？（可多选）',
    icon: <PieChart className="w-6 h-6" />,
    multiSelect: true,
    options: [
      { value: 'SUBJECTIVE_QM', label: '主观多头私募', description: '由管理人基于基本面深度研究选股的股票多头私募基金' },
      { value: 'QUANT_QM', label: '量化多头私募', description: '基于多因子/量价模型系统化选股的量化股票私募' },
      { value: 'INDEX_ENHANCE_QM', label: '指数增强私募', description: '跟踪沪深300、中证500等宽基指数并做超额增强的私募' },
      { value: 'STOCK_FUND', label: '股票型公募基金', description: '主动管理的股票型公募基金，全部投资A股' },
      { value: 'INDEX_FUND', label: '指数基金/ETF', description: '跟踪沪深300、中证500、创业板等A股宽基指数的被动产品' },
      { value: 'STOCK', label: '直接投资A股股票', description: '直接在沪深两市买卖个股' },
    ],
  },
  {
    key: 'decision_basis',
    title: '投资决策依据',
    subtitle: '您主要依据什么来做出投资决策？',
    icon: <LineChart className="w-6 h-6" />,
    options: [
      { value: 'RECOMMENDATION', label: '他人推荐', description: '主要参考亲朋好友或理财经理的推荐' },
      { value: 'NEWS', label: '新闻/政策', description: '根据财经新闻、政策动态进行投资决策' },
      { value: 'FUNDAMENTAL', label: '基本面分析', description: '研究上市公司财报、行业趋势等基本面信息' },
      { value: 'TECHNICAL', label: '技术分析', description: '通过K线图、技术指标等分析股价走势' },
      { value: 'QUANTITATIVE', label: '量化模型', description: '使用量化模型、因子分析等系统性方法进行投资' },
    ],
  },
  {
    key: 'risk_tolerance',
    title: '风险承受能力',
    subtitle: '根据实际情况选择您的风险承受等级（R1-R5）',
    icon: <Shield className="w-6 h-6" />,
    options: [
      { value: 'R1', label: 'C1-保守型', description: '本金安全第一，仅能承受极小净值回撤（5%以内），适合防御型红利价值等低波动股票策略' },
      { value: 'R2', label: 'C2-稳健型', description: '可接受小幅度回撤（5%-10%），适合红利价值多头、低波价值策略' },
      { value: 'R3', label: 'C3-平衡型', description: '能接受中等回撤（10%-15%），适合量化多头、指数增强策略' },
      { value: 'R4', label: 'C4-进取型', description: '能接受较大回撤（20%-30%），适合成长多头、行业主题策略' },
      { value: 'R5', label: 'C5-激进型', description: '能承受大幅回撤（30%以上），适合高成长/小盘等私募激进策略' },
    ],
  },
  {
    key: 'loss_attitude',
    title: '亏损应对态度',
    subtitle: '当您的投资组合出现亏损时，您通常的反应是？',
    icon: <AlertTriangle className="w-6 h-6" />,
    options: [
      { value: 'CUT_LOSS', label: '立即止损', description: '一旦亏损就立即卖出，控制损失在可接受范围内' },
      { value: 'REDUCE', label: '部分减仓', description: '适度降低仓位，但保留部分头寸等待反弹' },
      { value: 'HOLD', label: '持有不动', description: '相信长期价值，持有等待市场回升' },
      { value: 'AVERAGE_DOWN', label: '逢低加仓', description: '越跌越买，降低持仓成本' },
      { value: 'SWITCH', label: '切换品种', description: '卖出亏损品种，切换到其他看好的品种' },
    ],
  },
  {
    key: 'max_drawdown',
    title: '可接受最大亏损',
    subtitle: '您能接受的投资组合最大年度回撤是多少？',
    icon: <BarChart3 className="w-6 h-6" />,
    options: [
      { value: '3', label: '3%以内', description: '几乎不承受股票波动，类似现金管理' },
      { value: '5', label: '5%以内', description: '类似低波动红利价值策略，极小回撤' },
      { value: '10', label: '10%以内', description: '类似红利低波多头，可接受一定回撤' },
      { value: '15', label: '15%以内', description: '类似量化多头精选，中等回撤可接受' },
      { value: '25', label: '25%以内', description: '类似成长多头，较大回撤可接受' },
      { value: '30', label: '30%以上', description: '类似高弹性成长私募，大幅回撤可接受' },
    ],
  },
  {
    key: 'investment_horizon',
    title: '计划投资期限',
    subtitle: '您计划将这笔资金投资多长时间？',
    icon: <Clock className="w-6 h-6" />,
    options: [
      { value: 'SHORT', label: '短期（1年以内）', description: '资金1年内可能使用，需注意股票私募通常设有6-12个月份额锁定期，尽量选择低波动策略' },
      { value: 'MEDIUM', label: '中期（1-3年）', description: '适合多数股票多头私募，可覆盖份额锁定期并等待策略发挥' },
      { value: 'LONG', label: '长期（3-5年）', description: '资金规划3-5年内使用，利于价值/成长类私募策略体现长期回报' },
      { value: 'VERY_LONG', label: '超长期（5年以上）', description: '资金5年以上不使用，可充分享受股票多头复利与成长红利' },
    ],
  },
  {
    key: 'investment_objective',
    title: '主要投资目标',
    subtitle: '您进行投资的最主要目的是什么？',
    icon: <Target className="w-6 h-6" />,
    options: [
      { value: 'PRESERVATION', label: '资产保值', description: '以资产安全为首要目标，抵御通货膨胀，追求绝对收益' },
      { value: 'INCOME', label: '稳定收益', description: '追求稳定的利息或分红收益，改善现金流' },
      { value: 'BALANCED', label: '平衡配置', description: '在控制风险的前提下，追求资产稳健增长' },
      { value: 'GROWTH', label: '资产增值', description: '追求资产长期较大幅度增值，接受较高风险' },
    ],
  },
  {
    key: 'liquidity_requirement',
    title: '流动性需求',
    subtitle: '您对资金流动性的要求是？',
    icon: <Droplets className="w-6 h-6" />,
    options: [
      { value: 'HIGH', label: '随时可能动用', description: '资金随时可能需要使用，需要T+0或T+1变现' },
      { value: 'MEDIUM', label: '预计半年内使用', description: '资金可能在半年内使用，需要较高流动性' },
      { value: 'LOW', label: '1-3年内使用', description: '资金在1-3年内使用，可以接受定期投资' },
      { value: 'VERY_LOW', label: '3年以上使用', description: '资金3年以上才使用，可以进行长期锁定配置' },
    ],
  },
  {
    key: 'capital',
    title: '计划投资金额',
    subtitle: '您计划投入的资金规模是？（单位：人民币）',
    icon: <Wallet className="w-6 h-6" />,
    options: [
      { value: '50000', label: '5万元以下', description: '以股票/股票型基金小规模起步，建议先体验低波动策略' },
      { value: '200000', label: '5-20万元', description: '中等规模，可参与红利价值/量化多头等股票私募策略' },
      { value: '1000000', label: '20-100万元', description: '较大规模，可配置股票多头、指数增强等多策略组合' },
      { value: '10000000', label: '100万元以上', description: '已达相当规模，适合委托股票型私募基金公司进行专业化管理' },
    ],
  },
  {
    key: 'investment_style_preference',
    title: '投资风格偏好',
    subtitle: '作为股票型私募，请选择您偏好的多头投资风格（单选）',
    icon: <TrendingUp className="w-6 h-6" />,
    options: [
      { value: 'DIVIDEND', label: '红利价值', description: '投资高股息、低估值蓝筹股（如银行、煤炭、公用事业），获取分红+估值修复收益' },
      { value: 'VALUE', label: '深度价值', description: '投资PB/PE极低的价值股（如金融、地产、周期），等待价值回归' },
      { value: 'GROWTH', label: '成长投资', description: '投资高成长股（如新能源、半导体、医药），追求业绩增长带来的股价提升' },
      { value: 'QUALITY', label: '质量投资', description: '投资ROE高、护城河深的优质龙头（如消费、医药龙头），分享企业长期成长' },
      { value: 'INDEX', label: '指数投资', description: '通过沪深300、中证500、中证A500等宽基指数基金被动投资' },
      { value: 'BALANCED', label: '风格均衡', description: '不偏好特定风格，通过多因子模型均衡配置' },
    ],
  },
  {
    key: 'industry_preference',
    title: '行业配置偏好',
    subtitle: '您对A股哪些行业更感兴趣？（可多选）',
    icon: <Building2 className="w-6 h-6" />,
    multiSelect: true,
    options: [
      { value: 'CONSUMER', label: '消费/医药', description: '消费升级、医疗健康、食品饮料等长期发展行业' },
      { value: 'TECH', label: '科技/新能源', description: '人工智能、新能源车、半导体、光伏等成长赛道' },
      { value: 'FINANCE', label: '金融/地产', description: '银行、保险、房地产等价值周期行业' },
      { value: 'CYCLICAL', label: '周期/资源', description: '有色金属、煤炭、化工、钢铁等资源类行业' },
      { value: 'MANUFACTURING', label: '制造/工业', description: '高端制造、机械、军工、汽车等工业领域' },
      { value: 'BROAD', label: '均衡配置', description: '不偏好特定行业，进行行业均衡分散配置' },
    ],
  },
]

// 投资风格映射
interface InvestmentStyle {
  name: string
  description: string
  riskLevel: string
  riskRating: string
  expectedReturn: string
  maxDrawdown: string
  sharpeRatio: string
  assetAllocation: { label: string; value: number; color: string }[]
  industryAllocation: { label: string; value: number }[]
  suitableProducts: string[]
}

// 根据选择生成投资风格
function generateStyle(answers: Record<string, string>): InvestmentStyle {
  const experience = answers.investment_experience || 'NOVICE'
  const riskTolerance = answers.risk_tolerance || 'R2'
  const maxDrawdown = parseFloat(answers.max_drawdown || '10')
  const horizon = answers.investment_horizon || 'MEDIUM'
  const objective = answers.investment_objective || 'BALANCED'
  const stylePref = answers.investment_style_preference || 'BALANCED'
  const lossAttitude = answers.loss_attitude || 'HOLD'
  const capital = parseFloat(answers.capital || '100000')

  // 根据风险承受和最大回撤确定风格等级
  let styleLevel = 'balanced'
  let riskRating = 'R3'

  // 风险等级映射（参考基金公司C1-C5评级）
  if (riskTolerance === 'R1' || maxDrawdown <= 3) {
    styleLevel = 'conservative'
    riskRating = 'R1'
  } else if (riskTolerance === 'R2' || (maxDrawdown > 3 && maxDrawdown <= 10)) {
    styleLevel = 'conservative'
    riskRating = 'R2'
  } else if (riskTolerance === 'R3' || (maxDrawdown > 10 && maxDrawdown <= 15)) {
    styleLevel = 'balanced'
    riskRating = 'R3'
  } else if (riskTolerance === 'R4' || (maxDrawdown > 15 && maxDrawdown <= 25)) {
    styleLevel = 'aggressive'
    riskRating = 'R4'
  } else {
    styleLevel = 'aggressive'
    riskRating = 'R5'
  }

  // 根据投资期限调整风险等级
  if (horizon === 'SHORT' && styleLevel === 'aggressive') {
    styleLevel = 'balanced'
    riskRating = 'R3'
  }

  // 根据投资目标调整
  if (objective === 'PRESERVATION' && styleLevel === 'aggressive') {
    styleLevel = 'balanced'
    riskRating = 'R3'
  }

  // 根据投资经验调整（新手自动降级）
  if (experience === 'NOVICE' && styleLevel === 'aggressive') {
    styleLevel = 'balanced'
    riskRating = 'R3'
  }
  if (experience === 'NOVICE' && styleLevel === 'balanced' && riskRating === 'R3') {
    styleLevel = 'conservative'
    riskRating = 'R2'
  }

  // 根据亏损态度调整
  if (lossAttitude === 'CUT_LOSS' && styleLevel === 'aggressive') {
    styleLevel = 'balanced'
    riskRating = 'R3'
  }

  // 根据资金规模调整
  if (capital < 50000 && styleLevel === 'aggressive') {
    styleLevel = 'balanced'
    riskRating = 'R3'
  }

  // 构建资产配置（股票型私募：全部配置A股权益，仅预留少量现金做调仓机动）
  let allocation: { label: string; value: number; color: string }[] = []
  
  if (styleLevel === 'conservative') {
    allocation = [
      { label: '红利价值股', value: 70, color: '#3B82F6' },
      { label: '沪深300指数', value: 20, color: '#F59E0B' },
      { label: '现金', value: 10, color: '#10B981' },
    ]
  } else if (styleLevel === 'balanced') {
    allocation = [
      { label: '量化多头股', value: 55, color: '#8B5CF6' },
      { label: '中证500指数', value: 25, color: '#F59E0B' },
      { label: '现金', value: 20, color: '#10B981' },
    ]
  } else {
    allocation = [
      { label: '成长龙头股', value: 70, color: '#EC4899' },
      { label: '科创/创业板指', value: 20, color: '#F59E0B' },
      { label: '现金', value: 10, color: '#3B82F6' },
    ]
  }

  // 根据风格偏好调整资产配置
  if (stylePref === 'INDEX' && styleLevel !== 'conservative') {
    // 指数投资偏好：增加宽基指数配置
    allocation = allocation.map(item => {
      if (item.label === '量化多头股' || item.label === '成长龙头股') {
        return { ...item, label: '宽基指数', value: item.value }
      }
      return item
    })
  }

  // 行业配置建议
  const industryMap: Record<string, string[]> = {
    CONSUMER: ['消费/医药', '食品饮料', '医疗保健'],
    TECH: ['科技/新能源', '电子半导体', '电力设备'],
    FINANCE: ['金融/地产', '银行保险', '房地产'],
    CYCLICAL: ['周期/资源', '有色金属', '煤炭化工'],
    MANUFACTURING: ['高端制造', '机械军工', '汽车'],
    BROAD: ['均衡配置', '宽基指数', '多行业分散'],
  }

  // 根据风格偏好确定行业侧重
  const styleIndustryMap: Record<string, string[]> = {
    VALUE: ['银行保险', '有色金属', '煤炭'],
    DIVIDEND: ['公用事业', '银行', '交通运输'],
    GROWTH: ['电子半导体', '电力设备', '计算机'],
    QUALITY: ['食品饮料', '医药生物', '家用电器'],
    INDEX: ['沪深300', '中证500', '中证A500'],
    BALANCED: ['均衡配置'],
  }

  const industries = industryMap[answers.industry_preference || 'BROAD']
  const styleIndustries = styleIndustryMap[stylePref]
  
  // 构建行业配置（去重 + 总和100%）
  let industryAllocation: { label: string; value: number }[] = []
  const usedLabels = new Set<string>()
  const pushIndustry = (label: string, value: number) => {
    if (usedLabels.has(label) || value <= 0) return
    usedLabels.add(label)
    industryAllocation.push({ label, value })
  }

  if (styleIndustries && styleIndustries.length > 0) {
    const perValue = Math.floor(60 / styleIndustries.length)
    styleIndustries.forEach(ind => pushIndustry(ind, perValue))
    // 剩余配置给行业偏好（去重、避免0%）
    const remaining = 100 - industryAllocation.reduce((s, i) => s + i.value, 0)
    if (industries && industries.length > 0 && remaining > 0) {
      const extraIndustries = industries.filter(ind => !usedLabels.has(ind))
      if (extraIndustries.length > 0) {
        const extraPer = Math.floor(remaining / Math.min(extraIndustries.length, 3))
        extraIndustries.slice(0, 3).forEach(ind => pushIndustry(ind, extraPer))
      }
    }
  } else if (industries && industries.length > 0) {
    const perValue = Math.floor(100 / Math.min(industries.length, 5))
    industries.slice(0, 5).forEach(ind => pushIndustry(ind, perValue))
  }

  if (industryAllocation.length === 0) {
    industryAllocation = [
      { label: '宽基指数', value: 40 },
      { label: '消费医药', value: 20 },
      { label: '科技新能源', value: 20 },
      { label: '金融周期', value: 20 },
    ]
  }

  // 因取整产生的剩余权重补到首个行业，确保总和为100%
  const allocatedSum = industryAllocation.reduce((s, i) => s + i.value, 0)
  if (allocatedSum < 100) {
    industryAllocation[0].value += 100 - allocatedSum
  }

  // 根据风格等级生成最终方案（股票型私募多头方案）
  const styleMap: Record<string, InvestmentStyle> = {
    conservative: {
      name: '红利价值多头（C1/C2）',
      description: '股票型私募·防御型多头：以高股息、低波动的红利价值蓝筹股为核心，全部配置A股权益，仅预留少量现金，追求稳健回报。适合风险承受能力较低但仍参与股票投资的私募投资者，净值回撤相对可控。',
      riskLevel: '保守型',
      riskRating,
      expectedReturn: '7%-9%',
      maxDrawdown: '10%-16%',
      sharpeRatio: '0.8-1.2',
      assetAllocation: allocation,
      industryAllocation,
      suitableProducts: ['红利价值多头私募', '沪深300指数', '红利低波ETF'],
    },
    balanced: {
      name: '量化多因子多头（C3）',
      description: '股票型私募·量化多头：以质量、价值、动量、低波多因子全市场选股为核心，全仓A股权益，追求中长期超额收益。适合中等风险偏好的私募投资者，是多数股票私募的核心策略。',
      riskLevel: '平衡型',
      riskRating,
      expectedReturn: '8%-12%',
      maxDrawdown: '15%以内',
      sharpeRatio: '1.0-1.5',
      assetAllocation: allocation,
      industryAllocation,
      suitableProducts: ['量化多头私募', '指数增强私募', '中证500ETF'],
    },
    aggressive: {
      name: '成长龙头多头（C4/C5）',
      description: '股票型私募·成长多头：聚焦新能源、半导体、医药等高成长赛道龙头，满仓股票追求高弹性回报。短期净值波动较大，建议投资期限3年以上，适合能承受较大回撤的投资者。',
      riskLevel: '进取型',
      riskRating,
      expectedReturn: '12%-18%',
      maxDrawdown: '25%-30%',
      sharpeRatio: '0.8-1.3',
      assetAllocation: allocation,
      industryAllocation,
      suitableProducts: ['高成长私募', '行业主题私募', '科创50ETF'],
    },
  }

  const style = styleMap[styleLevel] || styleMap.balanced
  
  // 添加行业配置说明
  const industryNote = industries && industries.length > 0
    ? `（行业侧重：${industries.slice(0, 3).join('、')}）`
    : ''
  
  return {
    ...style,
    description: style.description + industryNote,
  }
}

export default function InvestmentPlanner() {
  const {
    profile,
    plan,
    candidates,
    isLoading,
    currentStep,
    setProfileAnswers,
    generatePlan,
    generateStockPool,
    approvePlan,
    resetPlanner,
  } = usePlannerStore()

  const [answers, setAnswers] = useState<Record<string, string>>({})
  const [multiAnswers, setMultiAnswers] = useState<Record<string, string[]>>({})
  const [currentQuestionIndex, setCurrentQuestionIndex] = useState(0)
  const [style, setStyle] = useState<InvestmentStyle | null>(null)
  const [showMyPlans, setShowMyPlans] = useState(false)

  const isInitializedRef = useRef(false)
  const styleRef = useRef<InvestmentStyle | null>(null)

  useEffect(() => { styleRef.current = style }, [style])

  // 初始化：加载已有答案（仅首次加载时执行完整解析）
  useEffect(() => {
    if (!profile?.profile) return
    if (isInitializedRef.current) return
    isInitializedRef.current = true

    const existingAnswers: Record<string, string> = {}
    const existingMultiAnswers: Record<string, string[]> = {}
    for (const q of CHOICE_QUESTIONS) {
      if (profile.profile[q.key]) {
        const val = String(profile.profile[q.key])
        if (q.multiSelect) {
          // 多选题：始终解析为数组，即使只有一个选项（无逗号）
          existingMultiAnswers[q.key] = val.split(',').filter(v => v.trim() !== '')
        } else {
          existingAnswers[q.key] = val
        }
      }
    }
    if (Object.keys(existingAnswers).length > 0 || Object.keys(existingMultiAnswers).length > 0) {
      setAnswers(existingAnswers)
      setMultiAnswers(existingMultiAnswers)
      // 找到第一个未回答的问题
      const firstUnanswered = CHOICE_QUESTIONS.findIndex((q) => {
        if (q.multiSelect) {
          return !existingMultiAnswers[q.key] || existingMultiAnswers[q.key].length === 0
        }
        return !existingAnswers[q.key]
      })
      if (firstUnanswered === -1) {
        // 所有问题已回答，自动生成投资风格
        setCurrentQuestionIndex(CHOICE_QUESTIONS.length)
        if (!styleRef.current) {
          const mergedAnswers = { ...existingAnswers }
          for (const [k, v] of Object.entries(existingMultiAnswers)) {
            mergedAnswers[k] = v.join(',')
          }
          const generatedStyle = generateStyle(mergedAnswers)
          setStyle(generatedStyle)
        }
      } else {
        setCurrentQuestionIndex(firstUnanswered)
      }
    }
  }, [profile])

  const handleSelect = (questionKey: string, value: string) => {
    const question = CHOICE_QUESTIONS.find(q => q.key === questionKey)
    if (!question) return

    if (question.multiSelect) {
      // 多选处理 - 只切换选择，不自动跳转（需要用户点击确认按钮）
      const currentMulti = multiAnswers[questionKey] || []
      const newMulti = currentMulti.includes(value)
        ? currentMulti.filter(v => v !== value)
        : [...currentMulti, value]
      
      setMultiAnswers({ ...multiAnswers, [questionKey]: newMulti })
      
      // 同步到 answers（用逗号分隔）
      const mergedAnswers = { ...answers, [questionKey]: newMulti.join(',') }
      setAnswers(mergedAnswers)
    } else {
      // 单选处理 - 立即保存并自动跳转下一题
      const newAnswers = { ...answers, [questionKey]: value }
      setAnswers(newAnswers)
      setProfileAnswers(newAnswers)

      // 单选已完成，延迟进入下一题，给用户视觉反馈时间
      setTimeout(() => {
        if (currentQuestionIndex < CHOICE_QUESTIONS.length - 1) {
          setCurrentQuestionIndex(currentQuestionIndex + 1)
        } else {
          // 完成所有问题，生成投资风格
          const generatedStyle = generateStyle(newAnswers)
          setStyle(generatedStyle)
        }
      }, 300)
    }
  }

  const handleMultiConfirm = (questionKey: string) => {
    // 多选确认后进入下一题
    const selected = multiAnswers[questionKey] || []
    if (selected.length === 0) return

    const mergedAnswers = { ...answers, [questionKey]: selected.join(',') }
    setAnswers(mergedAnswers)
    setProfileAnswers(mergedAnswers)

    if (currentQuestionIndex < CHOICE_QUESTIONS.length - 1) {
      setCurrentQuestionIndex(currentQuestionIndex + 1)
    } else {
      const generatedStyle = generateStyle(mergedAnswers)
      setStyle(generatedStyle)
    }
  }

  const handleBack = () => {
    if (currentQuestionIndex > 0) {
      setCurrentQuestionIndex(currentQuestionIndex - 1)
    }
  }

  const handleStyleConfirm = async () => {
    await generatePlan()
  }

  const handleApprove = async (_candidate: CandidateStrategy) => {
    if (plan) {
      await approvePlan(plan.planId, true)
    }
  }

  const handleReset = async () => {
    if (window.confirm('确定要重置投资规划吗？所有进度将丢失。')) {
      setAnswers({})
      setMultiAnswers({})
      setCurrentQuestionIndex(0)
      setStyle(null)
      await resetPlanner()
    }
  }

  const handleStartOver = () => {
    setAnswers({})
    setMultiAnswers({})
    setCurrentQuestionIndex(0)
    setStyle(null)
  }

  const currentQuestion = CHOICE_QUESTIONS[currentQuestionIndex]
  const completedCount = Object.keys(answers).filter(k => answers[k] !== '').length + 
    Object.keys(multiAnswers).filter(k => multiAnswers[k] && multiAnswers[k].length > 0).length
  const progress = (completedCount / CHOICE_QUESTIONS.length) * 100

  // 渲染不同步骤
  const renderContent = () => {
    // 已完成规划，显示审核界面
    if (currentStep === 'REVIEW' && plan) {
      return (
        <PlanReviewScreen
          plan={plan}
          candidates={candidates}
          onApprove={handleApprove}
          generateStockPool={generateStockPool}
          isLoading={isLoading}
        />
      )
    }

    // 规划生成中
    if (currentStep === 'PLANNING') {
      return <PlanningScreen />
    }

    // 已完成
    if (currentStep === 'COMPLETE' && plan) {
      return <CompleteScreen plan={plan} />
    }

    // 投资风格已生成，优先显示风格卡片（防止因currentQuestionIndex被重置导致循环）
    if (style) {
      return (
        <StyleCard
          style={style}
          answers={answers}
          onConfirm={handleStyleConfirm}
          onEdit={handleStartOver}
          isLoading={isLoading}
        />
      )
    }

    // 选择题模式
    if (currentQuestionIndex < CHOICE_QUESTIONS.length) {
      const selectedValue = answers[currentQuestion.key]
      const multiSelectedValues = multiAnswers[currentQuestion.key] || []
      
      return (
        <ChoiceQuestionCard
          question={currentQuestion}
          selectedValue={selectedValue}
          multiSelectedValues={multiSelectedValues}
          onSelect={(value) => handleSelect(currentQuestion.key, value)}
          onConfirm={() => handleMultiConfirm(currentQuestion.key)}
          onBack={handleBack}
          canGoBack={currentQuestionIndex > 0}
          questionIndex={currentQuestionIndex}
          totalQuestions={CHOICE_QUESTIONS.length}
        />
      )
    }

    return null
  }

  return (
    <div className="h-full flex flex-col bg-slate-50 dark:bg-slate-900">
      {/* 顶部标题区 */}
      <div className="bg-white dark:bg-slate-800 border-b border-slate-200 dark:border-slate-700 px-6 py-4">
        <div className="flex items-center justify-between">
          <div className="flex items-center gap-3">
            <div className="w-10 h-10 rounded-xl bg-gradient-to-br from-brand-500 to-cyan-400 flex items-center justify-center shadow-sm">
              <Sparkles className="w-5 h-5 text-white" />
            </div>
            <div>
              <h1 className="text-lg font-bold text-slate-800 dark:text-slate-100">投资规划</h1>
              <p className="text-xs text-slate-500 dark:text-slate-400">A股专属 · 专业级投资适当性评估</p>
            </div>
          </div>
          
          <div className="flex items-center gap-2">
            <button
              onClick={() => setShowMyPlans(true)}
              className="flex items-center gap-1.5 px-3 py-1.5 text-xs text-brand-600 hover:text-brand-700 dark:text-brand-400 dark:hover:text-brand-300 border border-brand-200 dark:border-brand-800 rounded-lg hover:bg-brand-50 dark:hover:bg-brand-900/30 transition-colors"
            >
              <FolderOpen className="w-3.5 h-3.5" />
              我的投资方案
            </button>
            <button
              onClick={handleReset}
              className="flex items-center gap-1.5 px-3 py-1.5 text-xs text-slate-500 hover:text-slate-700 dark:text-slate-400 dark:hover:text-slate-200 border border-slate-200 dark:border-slate-700 rounded-lg hover:bg-slate-50 dark:hover:bg-slate-700 transition-colors"
            >
              <RotateCcw className="w-3.5 h-3.5" />
              重新启动投资问答
            </button>
          </div>
        </div>

        {/* 进度条 */}
        {currentQuestionIndex < CHOICE_QUESTIONS.length && !style && (
          <div className="mt-4">
            <div className="flex justify-between text-sm mb-2">
              <span className="text-slate-600 dark:text-slate-300">
                进度: {Math.min(completedCount, CHOICE_QUESTIONS.length)} / {CHOICE_QUESTIONS.length} 题
              </span>
              <span className="font-medium text-brand-600 dark:text-brand-400">
                {Math.round(progress)}%
              </span>
            </div>
            <div className="h-2 bg-slate-200 dark:bg-slate-700 rounded-full overflow-hidden">
              <div 
                className="h-full bg-gradient-to-r from-brand-500 to-purple-500 transition-all duration-500"
                style={{ width: `${progress}%` }}
              />
            </div>
          </div>
        )}
      </div>

      {/* 主内容区 */}
      <div className="flex-1 overflow-y-auto px-6 py-6">
        <div className="max-w-3xl mx-auto">
          {renderContent()}
        </div>
      </div>

      {/* 我的投资方案弹窗 */}
      {showMyPlans && <MyPlansModal onClose={() => setShowMyPlans(false)} />}
    </div>
  )
}

// ==================== 我的投资方案 ====================

const PLAN_STATUS_LABELS: Record<string, { label: string; color: string }> = {
  DRAFT: { label: '草稿', color: 'bg-slate-100 text-slate-600 dark:bg-slate-700 dark:text-slate-300' },
  GENERATING: { label: '生成中', color: 'bg-blue-100 text-blue-600 dark:bg-blue-900/40 dark:text-blue-300' },
  REVIEWING: { label: 'CIO审核中', color: 'bg-amber-100 text-amber-600 dark:bg-amber-900/40 dark:text-amber-300' },
  PENDING_APPROVAL: { label: '待批准', color: 'bg-amber-100 text-amber-600 dark:bg-amber-900/40 dark:text-amber-300' },
  APPROVED: { label: '已批准', color: 'bg-green-100 text-green-600 dark:bg-green-900/40 dark:text-green-300' },
  ACTIVE: { label: '当前生效', color: 'bg-emerald-100 text-emerald-700 dark:bg-emerald-900/40 dark:text-emerald-300' },
  RUNNING: { label: '运行中', color: 'bg-emerald-100 text-emerald-600 dark:bg-emerald-900/40 dark:text-emerald-300' },
  PAUSED: { label: '已暂停', color: 'bg-slate-100 text-slate-600 dark:bg-slate-700 dark:text-slate-300' },
  REJECTED: { label: '已拒绝', color: 'bg-red-100 text-red-600 dark:bg-red-900/40 dark:text-red-300' },
  ARCHIVED: { label: '已归档', color: 'bg-slate-100 text-slate-600 dark:bg-slate-700 dark:text-slate-300' },
}

function MyPlansModal({ onClose }: { onClose: () => void }) {
  const { plans, loadingPlans, listPlans, getPlanDetail } = usePlannerStore()
  const [selectedPlan, setSelectedPlan] = useState<InvestmentPlan | null>(null)
  const [loadingDetail, setLoadingDetail] = useState(false)
  const [isCIOReviewing, setIsCIOReviewing] = useState(false)

  useEffect(() => {
    listPlans()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  const handleSelect = async (planId: string) => {
    setLoadingDetail(true)
    const detail = await getPlanDetail(planId)
    setSelectedPlan(detail)
    setLoadingDetail(false)
  }

  const handleCIOReview = async () => {
    if (!selectedPlan?.planId) return
    setIsCIOReviewing(true)
    try {
      const { CIOReviewPlan } = await import('../../wailsjs/go/main/App')
      const result = await CIOReviewPlan(selectedPlan.planId) as unknown as { approved: boolean }
      // 重新加载方案详情
      const updatedPlan = await getPlanDetail(selectedPlan.planId)
      setSelectedPlan(updatedPlan)
      // 如果审核通过，刷新方案列表
      if (result?.approved) {
        listPlans()
      }
    } catch (err) {
      console.error('CIO review failed:', err)
    } finally {
      setIsCIOReviewing(false)
    }
  }

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center p-4 bg-black/50 backdrop-blur-sm">
      <div className="w-full max-w-3xl max-h-[85vh] flex flex-col bg-white dark:bg-slate-800 rounded-2xl shadow-2xl overflow-hidden">
        {/* 弹窗头部 */}
        <div className="flex items-center justify-between px-6 py-4 border-b border-slate-200 dark:border-slate-700 bg-white dark:bg-slate-800">
          <div className="flex items-center gap-3">
            {selectedPlan ? (
              <button
                onClick={() => setSelectedPlan(null)}
                className="flex items-center gap-1.5 text-xs text-slate-500 hover:text-slate-700 dark:text-slate-400 dark:hover:text-slate-200 transition-colors"
              >
                <ArrowLeft className="w-4 h-4" />
                返回列表
              </button>
            ) : (
              <FolderOpen className="w-5 h-5 text-brand-600 dark:text-brand-400" />
            )}
            <h2 className="text-base font-bold text-slate-800 dark:text-slate-100">
              {selectedPlan ? '投资方案详情' : '我的投资方案'}
            </h2>
          </div>
          <button
            onClick={onClose}
            className="p-1.5 rounded-lg text-slate-400 hover:text-slate-600 dark:hover:text-slate-200 hover:bg-slate-100 dark:hover:bg-slate-700 transition-colors"
          >
            <X className="w-5 h-5" />
          </button>
        </div>

        {/* 弹窗内容 */}
        <div className="flex-1 overflow-y-auto px-6 py-5">
          {selectedPlan ? (
            <PlanDetailView plan={selectedPlan} onCIOReview={handleCIOReview} isReviewing={isCIOReviewing} />
          ) : loadingPlans ? (
            <div className="flex flex-col items-center justify-center py-16">
              <div className="w-10 h-10 border-3 border-brand-600 border-t-transparent rounded-full animate-spin mb-4" />
              <p className="text-sm text-slate-500">正在加载投资方案...</p>
            </div>
          ) : plans.length === 0 ? (
            <div className="flex flex-col items-center justify-center py-16 text-center">
              <div className="w-14 h-14 rounded-2xl bg-slate-100 dark:bg-slate-700 flex items-center justify-center mb-4">
                <FolderOpen className="w-7 h-7 text-slate-400" />
              </div>
              <p className="text-base font-medium text-slate-700 dark:text-slate-200 mb-1">暂无投资方案</p>
              <p className="text-sm text-slate-500 dark:text-slate-400">完成投资规划后，您的投资方案将展示在这里</p>
            </div>
          ) : (
            <div className="space-y-3">
              {plans.map((p) => (
                <button
                  key={p.planId}
                  onClick={() => handleSelect(p.planId)}
                  className="w-full text-left bg-white dark:bg-slate-900/40 border border-slate-200 dark:border-slate-700 rounded-xl p-4 hover:border-brand-300 dark:hover:border-brand-700 hover:shadow-sm transition-all group"
                >
                  <div className="flex items-start justify-between gap-3">
                    <div className="flex-1 min-w-0">
                      <div className="flex items-center gap-2 mb-1">
                        <span className="font-semibold text-slate-800 dark:text-slate-100 truncate">{p.name || '未命名方案'}</span>
                        <span className={`shrink-0 text-xs px-2 py-0.5 rounded-full ${(PLAN_STATUS_LABELS[p.status] || PLAN_STATUS_LABELS.DRAFT).color}`}>
                          {(PLAN_STATUS_LABELS[p.status] || PLAN_STATUS_LABELS.DRAFT).label}
                        </span>
                      </div>
                      <p className="text-xs text-slate-500 dark:text-slate-400 truncate">{p.objective || '—'}</p>
                    </div>
                    <ChevronRight className="w-4 h-4 text-slate-300 group-hover:text-brand-500 shrink-0 mt-1" />
                  </div>
                  <div className="flex flex-wrap items-center gap-x-4 gap-y-1 mt-3 text-xs text-slate-500 dark:text-slate-400">
                    <span className="flex items-center gap-1">
                      <TrendingUp className="w-3 h-3" />
                      {riskLabel(p.riskLevel)}
                    </span>
                    <span className="flex items-center gap-1">
                      <Target className="w-3 h-3" />
                      目标收益 {p.targetReturn ? `${(p.targetReturn * 100).toFixed(0)}%` : '—'}
                    </span>
                    <span className="flex items-center gap-1">
                      <Layers className="w-3 h-3" />
                      股票池 {p.stockPool?.submittedCount ?? 0} 只
                    </span>
                    <span className="flex items-center gap-1 ml-auto">
                      <Calendar className="w-3 h-3" />
                      {p.createdAt ? new Date(p.createdAt).toLocaleDateString('zh-CN') : '—'}
                    </span>
                  </div>
                </button>
              ))}
            </div>
          )}
        </div>
      </div>
    </div>
  )
}

// 投资方案详情（只读）
function PlanDetailView({ plan, onCIOReview, isReviewing }: { plan: InvestmentPlan; onCIOReview?: () => Promise<void>; isReviewing?: boolean }) {
  const mandate = plan.mandate
  const construction = plan.construction
  const riskLabels: Record<string, string> = {
    conservative: '保守型',
    balanced: '平衡型',
    growth: '积极型',
  }

  // 检查方案状态
  const isReviewingState = plan.status === 'REVIEWING'
  const isRejected = plan.status === 'REJECTED'

  return (
    <div className="space-y-6">
      {/* CIO 审核状态提示 */}
      {isReviewingState && (
        <div className="bg-amber-50 dark:bg-amber-900/20 border border-amber-200 dark:border-amber-800 rounded-xl p-4">
          <div className="flex items-center justify-between">
            <div className="flex items-center gap-2">
              <div className="w-4 h-4 border-2 border-amber-500 border-t-transparent rounded-full animate-spin" />
              <span className="text-sm font-medium text-amber-800 dark:text-amber-200">AI CIO 正在审核此投资方案...</span>
            </div>
            {onCIOReview && (
              <button
                onClick={onCIOReview}
                disabled={isReviewing}
                className="text-xs px-3 py-1.5 bg-amber-500 text-white rounded-lg hover:bg-amber-600 transition-colors disabled:opacity-50 disabled:cursor-not-allowed"
              >
                {isReviewing ? '审核中...' : '重新审核'}
              </button>
            )}
          </div>
          <p className="text-xs text-amber-600 dark:text-amber-300 mt-2">
            CIO 将根据您的风险承受能力和投资目标，自动分析方案的合理性并做出批准或拒绝决定。
          </p>
        </div>
      )}

      {/* 已拒绝提示 */}
      {isRejected && (
        <div className="bg-red-50 dark:bg-red-900/20 border border-red-200 dark:border-red-800 rounded-xl p-4">
          <div className="flex items-center gap-2">
            <AlertTriangle className="w-5 h-5 text-red-500" />
            <span className="text-sm font-medium text-red-800 dark:text-red-200">此投资方案已被 CIO 拒绝</span>
          </div>
          <p className="text-xs text-red-600 dark:text-red-300 mt-2">
            您可以重新生成新的投资方案，或调整投资目标和风险承受能力后再次尝试。
          </p>
        </div>
      )}

      {/* 方案概要 */}
      <div className="bg-gradient-to-br from-brand-50 to-purple-50 dark:from-brand-900/20 dark:to-purple-900/20 rounded-xl p-5">
        <div className="flex items-center justify-between mb-3">
          <div>
            <h3 className="text-lg font-bold text-slate-800 dark:text-slate-100">{plan.name || '未命名方案'}</h3>
            <p className="text-xs text-slate-500 mt-0.5">{plan.objective || ''}</p>
          </div>
          <span className={`shrink-0 text-xs px-2.5 py-1 rounded-full ${(PLAN_STATUS_LABELS[plan.status] || PLAN_STATUS_LABELS.DRAFT).color}`}>
            {(PLAN_STATUS_LABELS[plan.status] || PLAN_STATUS_LABELS.DRAFT).label}
          </span>
        </div>
        <div className="grid grid-cols-2 md:grid-cols-4 gap-3">
          <div className="bg-white dark:bg-slate-800 rounded-lg p-3">
            <div className="text-xs text-slate-500 mb-1">风险等级</div>
            <div className="text-base font-bold text-slate-800 dark:text-slate-100">{riskLabels[plan.riskLevel] || plan.riskLevel}</div>
          </div>
          <div className="bg-white dark:bg-slate-800 rounded-lg p-3">
            <div className="text-xs text-slate-500 mb-1">目标收益</div>
            <div className="text-base font-bold text-green-600">{plan.targetReturn ? `${(plan.targetReturn * 100).toFixed(0)}%` : '—'}</div>
          </div>
          <div className="bg-white dark:bg-slate-800 rounded-lg p-3">
            <div className="text-xs text-slate-500 mb-1">目标波动率</div>
            <div className="text-base font-bold text-slate-800 dark:text-slate-100">{plan.targetVolatility ? `${(plan.targetVolatility * 100).toFixed(0)}%` : '—'}</div>
          </div>
          <div className="bg-white dark:bg-slate-800 rounded-lg p-3">
            <div className="text-xs text-slate-500 mb-1">最大回撤</div>
            <div className="text-base font-bold text-orange-600">{plan.maxDrawdown ? `≤${(plan.maxDrawdown * 100).toFixed(0)}%` : '—'}</div>
          </div>
        </div>
      </div>

      {/* 投资任务书 */}
      {mandate && (
        <div className="bg-white dark:bg-slate-900/40 border border-slate-200 dark:border-slate-700 rounded-xl p-5">
          <div className="flex items-center gap-2 mb-4">
            <FileText className="w-4 h-4 text-brand-500" />
            <h4 className="text-sm font-bold text-slate-800 dark:text-slate-100">投资任务书（Investment Mandate）</h4>
          </div>
          <div className="grid grid-cols-2 md:grid-cols-4 gap-3">
            <MandateMetric
              label="目标收益区间"
              value={`${(mandate.targetReturnRange[0] * 100).toFixed(0)}–${(mandate.targetReturnRange[1] * 100).toFixed(0)}%`}
              icon={<TrendingUp className="w-4 h-4" />}
              color="text-green-600"
            />
            <MandateMetric
              label="目标波动率"
              value={`≤${(mandate.targetVolatility * 100).toFixed(0)}%`}
              icon={<Activity className="w-4 h-4" />}
              color="text-blue-600"
            />
            <MandateMetric
              label="最大回撤"
              value={`≤${(mandate.maxDrawdown * 100).toFixed(0)}%`}
              icon={<AlertTriangle className="w-4 h-4" />}
              color="text-orange-600"
            />
            <MandateMetric
              label="单只持仓上限"
              value={`${(mandate.positionLimit.maxSingleStock * 100).toFixed(0)}%`}
              icon={<Shield className="w-4 h-4" />}
              color="text-purple-600"
            />
          </div>
        </div>
      )}

      {/* 资产配置 */}
      {construction && (
        <div className="bg-white dark:bg-slate-900/40 border border-slate-200 dark:border-slate-700 rounded-xl p-5">
          <div className="flex items-center gap-2 mb-4">
            <PieChart className="w-4 h-4 text-brand-500" />
            <h4 className="text-sm font-bold text-slate-800 dark:text-slate-100">资产配置（Portfolio Construction）</h4>
          </div>
          <div className="flex h-4 rounded-lg overflow-hidden mb-2">
            <div className="bg-blue-500" style={{ width: `${construction.equityWeight * 100}%` }} />
            <div className="bg-purple-500" style={{ width: `${construction.fundWeight * 100}%` }} />
            <div className="bg-slate-400" style={{ width: `${construction.cashWeight * 100}%` }} />
          </div>
          <div className="flex justify-between text-xs text-slate-500 mb-4">
            <span>股票 {(construction.equityWeight * 100).toFixed(0)}%</span>
            <span>基金 {(construction.fundWeight * 100).toFixed(0)}%</span>
            <span>现金 {(construction.cashWeight * 100).toFixed(0)}%</span>
          </div>
          {construction.positions && construction.positions.length > 0 && (
            <div className="grid grid-cols-1 sm:grid-cols-2 gap-2">
              {construction.positions.map((pos: any, i: number) => (
                <div key={i} className="flex items-center justify-between bg-slate-50 dark:bg-slate-800 rounded-lg px-3 py-2 text-sm">
                  <span className="text-slate-700 dark:text-slate-200">{pos.assetName || pos.assetCode || `标的 ${i + 1}`}</span>
                  <span className="font-medium text-slate-500">{pos.targetWeight ? `${(pos.targetWeight * 100).toFixed(1)}%` : ''}</span>
                </div>
              ))}
            </div>
          )}
        </div>
      )}

      {/* 候选策略 */}
      {plan.candidates && plan.candidates.length > 0 && (
        <div className="bg-white dark:bg-slate-900/40 border border-slate-200 dark:border-slate-700 rounded-xl p-5">
          <div className="flex items-center gap-2 mb-4">
            <BarChart3 className="w-4 h-4 text-brand-500" />
            <h4 className="text-sm font-bold text-slate-800 dark:text-slate-100">候选策略（Candidates）</h4>
          </div>
          <div className="grid grid-cols-1 sm:grid-cols-3 gap-3">
            {plan.candidates.map((c) => (
              <div key={c.candidateId || c.id} className="bg-slate-50 dark:bg-slate-800 rounded-xl p-4">
                <div className="flex items-center justify-between mb-2">
                  <span className="text-sm font-semibold text-slate-800 dark:text-slate-100">{c.label}</span>
                  <span className="text-xs text-slate-500">{c.name}</span>
                </div>
                <div className="grid grid-cols-2 gap-2 text-center">
                  <div>
                    <div className="text-xs text-slate-500">预期收益</div>
                    <div className="font-medium text-green-600">{c.expectedReturn ? `${(c.expectedReturn * 100).toFixed(1)}%` : '—'}</div>
                  </div>
                  <div>
                    <div className="text-xs text-slate-500">夏普</div>
                    <div className="font-medium text-slate-700 dark:text-slate-200">{c.sharpe ? c.sharpe.toFixed(2) : '—'}</div>
                  </div>
                  <div>
                    <div className="text-xs text-slate-500">最大回撤</div>
                    <div className="font-medium text-orange-600">{c.maxDrawdown ? `${(c.maxDrawdown * 100).toFixed(1)}%` : '—'}</div>
                  </div>
                  <div>
                    <div className="text-xs text-slate-500">风险评分</div>
                    <div className="font-medium text-slate-700 dark:text-slate-200">{c.riskScore ? c.riskScore.toFixed(1) : '—'}</div>
                  </div>
                </div>
              </div>
            ))}
          </div>
        </div>
      )}

      {/* 股票池 */}
      {plan.stockPool && (
        <div className="bg-white dark:bg-slate-900/40 border border-slate-200 dark:border-slate-700 rounded-xl p-5">
          <div className="flex items-center gap-2 mb-4">
            <Layers className="w-4 h-4 text-brand-500" />
            <h4 className="text-sm font-bold text-slate-800 dark:text-slate-100">智能选股股票池</h4>
          </div>
          <div className="grid grid-cols-2 md:grid-cols-4 gap-3">
            <div className="bg-slate-50 dark:bg-slate-800 rounded-lg p-3">
              <div className="text-xs text-slate-500 mb-1">策略模板</div>
              <div className="text-lg font-bold text-slate-800 dark:text-slate-100">
                {({ balanced: '均衡配置', defensive: '防御型', growth: '成长型' } as Record<string, string>)[plan.stockPool.strategyId] || plan.stockPool.strategyId}
              </div>
            </div>
            <div className="bg-slate-50 dark:bg-slate-800 rounded-lg p-3">
              <div className="text-xs text-slate-500 mb-1">已纳入股票池</div>
              <div className="text-lg font-bold text-brand-600">{plan.stockPool.submittedCount} 只</div>
            </div>
            <div className="bg-slate-50 dark:bg-slate-800 rounded-lg p-3">
              <div className="text-xs text-slate-500 mb-1">引擎筛选结果</div>
              <div className="text-lg font-bold text-slate-800 dark:text-slate-100">{plan.stockPool.totalResults} 只</div>
            </div>
            <div className="bg-slate-50 dark:bg-slate-800 rounded-lg p-3">
              <div className="text-xs text-slate-500 mb-1">生成时间</div>
              <div className="text-sm font-medium text-slate-700 dark:text-slate-300">
                {plan.stockPool.generatedAt ? new Date(plan.stockPool.generatedAt).toLocaleString('zh-CN') : '—'}
              </div>
            </div>
          </div>
        </div>
      )}

      <div className="bg-amber-50 dark:bg-amber-900/20 border border-amber-200 dark:border-amber-800 rounded-lg p-3">
        <p className="text-xs text-amber-800 dark:text-amber-200">
          ⚠️ 本方案仅供参考，不构成投资建议。A股市场有风险，投资需谨慎。
        </p>
      </div>
    </div>
  )
}

function riskLabel(riskLevel: string): string {
  const map: Record<string, string> = {
    conservative: '保守型',
    balanced: '平衡型',
    growth: '积极型',
  }
  return map[riskLevel] || riskLevel || '—'
}

// ==================== 选择题组件 ====================

function ChoiceQuestionCard({ 
  question, 
  selectedValue,
  multiSelectedValues,
  onSelect,
  onConfirm,
  onBack, 
  canGoBack,
  questionIndex,
  totalQuestions
}: { 
  question: ChoiceQuestion
  selectedValue?: string
  multiSelectedValues?: string[]
  onSelect: (value: string) => void
  onConfirm: () => void
  onBack: () => void
  canGoBack: boolean
  questionIndex: number
  totalQuestions: number
}) {
  const isMulti = question.multiSelect
  const multiCount = (multiSelectedValues || []).length

  return (
    <div className="space-y-6">
      {/* 问题头部 */}
      <div className="text-center">
        <div className="inline-flex items-center justify-center w-16 h-16 rounded-2xl bg-gradient-to-br from-brand-100 to-purple-100 dark:from-brand-900/30 dark:to-purple-900/30 text-brand-600 dark:text-brand-400 mb-4">
          {question.icon}
        </div>
        <h2 className="text-xl font-bold text-slate-800 dark:text-slate-100 mb-2">
          {question.title}
        </h2>
        <p className="text-slate-600 dark:text-slate-400">
          {question.subtitle}
        </p>
        {isMulti && (
          <p className="text-xs text-brand-600 dark:text-brand-400 mt-2">
            （多选题，已选 {multiCount} 项）
          </p>
        )}
      </div>

      {/* 选项 */}
      <div className="space-y-3">
        {question.options.map((option) => {
          const isSelected = isMulti
            ? (multiSelectedValues || []).includes(option.value)
            : selectedValue === option.value

          return (
            <button
              key={option.value}
              onClick={() => onSelect(option.value)}
              className={`w-full p-4 text-left rounded-xl border-2 transition-all ${
                isSelected
                  ? 'border-brand-500 bg-brand-50 dark:bg-brand-900/20'
                  : 'border-slate-200 dark:border-slate-700 hover:border-brand-300 dark:hover:border-brand-700 hover:bg-slate-50 dark:hover:bg-slate-800'
              }`}
            >
              <div className="flex items-start gap-3">
                <div className={`w-5 h-5 rounded-full border-2 flex-shrink-0 mt-0.5 flex items-center justify-center ${
                  isSelected
                    ? 'border-brand-500 bg-brand-500'
                    : 'border-slate-300 dark:border-slate-600'
                }`}>
                  {isSelected && (
                    <CheckCircle className="w-3 h-3 text-white" />
                  )}
                </div>
                <div className="flex-1">
                  <div className="font-medium text-slate-800 dark:text-slate-100">
                    {option.label}
                  </div>
                  {option.description && (
                    <div className="text-sm text-slate-500 dark:text-slate-400 mt-1">
                      {option.description}
                    </div>
                  )}
                </div>
                <ChevronRight className="w-5 h-5 text-slate-400" />
              </div>
            </button>
          )
        })}
      </div>

      {/* 多选确认按钮 */}
      {isMulti && multiCount > 0 && (
        <div className="text-center">
          <button
            onClick={onConfirm}
            className="px-6 py-2 bg-brand-600 text-white rounded-lg font-medium hover:bg-brand-700 transition-colors"
          >
            确认选择 →
          </button>
        </div>
      )}

      {/* 导航按钮 */}
      {canGoBack && (
        <div className="text-center">
          <button
            onClick={onBack}
            className="text-sm text-slate-500 hover:text-slate-700 dark:text-slate-400 dark:hover:text-slate-200 underline"
          >
            ← 返回上一题
          </button>
        </div>
      )}

      {/* 题目计数 */}
      <div className="text-center text-xs text-slate-400">
        第 {questionIndex + 1} / {totalQuestions} 题
      </div>
    </div>
  )
}

// ==================== 投资风格卡片 ====================

function StyleCard({ 
  style, 
  answers, 
  onConfirm, 
  onEdit,
  isLoading 
}: { 
  style: InvestmentStyle
  answers: Record<string, string>
  onConfirm: () => void
  onEdit: () => void
  isLoading: boolean
}) {
  return (
    <div className="space-y-6">
      {/* 头部 */}
      <div className="text-center">
        <div className="inline-flex items-center justify-center w-16 h-16 rounded-2xl bg-gradient-to-br from-green-100 to-emerald-100 dark:from-green-900/30 dark:to-emerald-900/30 text-green-600 dark:text-green-400 mb-4">
          <Sparkles className="w-8 h-8" />
        </div>
        <h2 className="text-xl font-bold text-slate-800 dark:text-slate-100 mb-2">
          您的A股投资风格
        </h2>
        <p className="text-slate-600 dark:text-slate-400">
          根据您的选择，我们为您生成了专属A股投资风格方案
        </p>
      </div>

      {/* 风格卡片 */}
      <div className="bg-gradient-to-br from-brand-50 to-purple-50 dark:from-brand-900/20 dark:to-purple-900/20 rounded-2xl p-6">
        <div className="flex items-center justify-between mb-4">
          <h3 className="text-2xl font-bold text-slate-800 dark:text-slate-100">
            {style.name}
          </h3>
          <span className={`px-3 py-1 rounded-full text-sm font-medium ${
            style.riskLevel === '保守型' ? 'bg-green-100 text-green-700 dark:bg-green-900/30 dark:text-green-400' :
            style.riskLevel === '平衡型' ? 'bg-blue-100 text-blue-700 dark:bg-blue-900/30 dark:text-blue-400' :
            'bg-orange-100 text-orange-700 dark:bg-orange-900/30 dark:text-orange-400'
          }`}>
            {style.riskLevel}
          </span>
        </div>
        
        <p className="text-slate-600 dark:text-slate-400 mb-6">
          {style.description}
        </p>

        {/* 关键指标 */}
        <div className="grid grid-cols-3 gap-4 mb-6">
          <div className="text-center">
            <div className="text-2xl font-bold text-slate-800 dark:text-slate-100">
              {style.expectedReturn}
            </div>
            <div className="text-xs text-slate-500 dark:text-slate-400">预期年化收益</div>
          </div>
          <div className="text-center">
            <div className="text-2xl font-bold text-slate-800 dark:text-slate-100">
              {style.maxDrawdown}
            </div>
            <div className="text-xs text-slate-500 dark:text-slate-400">最大回撤</div>
          </div>
          <div className="text-center">
            <div className="text-2xl font-bold text-slate-800 dark:text-slate-100">
              {style.riskLevel}
            </div>
            <div className="text-xs text-slate-500 dark:text-slate-400">风险等级</div>
          </div>
        </div>

        {/* 资产配置 */}
        <div className="mb-6">
          <h4 className="text-sm font-medium text-slate-700 dark:text-slate-300 mb-3">资产配置建议（公募基金）</h4>
          <div className="flex h-8 rounded-lg overflow-hidden mb-3">
            {style.assetAllocation.map((item, idx) => (
              <div
                key={idx}
                className={`flex items-center justify-center text-xs text-white font-medium ${
                  idx === 0 ? 'bg-blue-500' :
                  idx === 1 ? 'bg-purple-500' :
                  idx === 2 ? 'bg-green-500' :
                  'bg-orange-500'
                }`}
                style={{ width: `${item.value}%` }}
                title={`${item.label}: ${item.value}%`}
              >
                {item.value >= 10 ? `${item.value}%` : ''}
              </div>
            ))}
          </div>
          <div className="flex flex-wrap gap-3 text-xs">
            {style.assetAllocation.map((item, idx) => (
              <div key={idx} className="flex items-center gap-1">
                <div className={`w-3 h-3 rounded-sm ${
                  idx === 0 ? 'bg-blue-500' :
                  idx === 1 ? 'bg-purple-500' :
                  idx === 2 ? 'bg-green-500' :
                  'bg-orange-500'
                }`} />
                <span className="text-slate-600 dark:text-slate-400">{item.label} {item.value}%</span>
              </div>
            ))}
          </div>
        </div>

        {/* 行业配置 */}
        <div className="mb-6">
          <h4 className="text-sm font-medium text-slate-700 dark:text-slate-300 mb-3">A股行业配置建议</h4>
          <div className="grid grid-cols-3 gap-2">
            {style.industryAllocation.map((item, idx) => (
              <div key={idx} className="bg-white dark:bg-slate-800 rounded-lg p-2 text-center">
                <div className="text-sm font-medium text-slate-700 dark:text-slate-300">{item.label}</div>
                <div className="text-xs text-brand-600 dark:text-brand-400">{item.value}%</div>
              </div>
            ))}
          </div>
        </div>

        {/* 推荐产品 */}
        <div>
          <h4 className="text-sm font-medium text-slate-700 dark:text-slate-300 mb-2">适合的A股产品类型</h4>
          <div className="flex flex-wrap gap-2">
            {style.suitableProducts.map((product, idx) => (
              <span key={idx} className="px-3 py-1 bg-white dark:bg-slate-800 rounded-full text-xs text-slate-700 dark:text-slate-300 border border-slate-200 dark:border-slate-600">
                {product}
              </span>
            ))}
          </div>
        </div>
      </div>

      {/* 用户选择摘要 */}
      <div className="bg-white dark:bg-slate-800 rounded-xl p-4 border border-slate-200 dark:border-slate-700">
        <h4 className="text-sm font-medium text-slate-700 dark:text-slate-300 mb-3">您的选择摘要</h4>
        <div className="grid grid-cols-2 gap-2 text-sm">
          {Object.entries(answers).map(([key, value]) => {
            const question = CHOICE_QUESTIONS.find(q => q.key === key)
            if (!question) return null
            
            let displayValue = value
            if (question.multiSelect && value.includes(',')) {
              const labels = value.split(',').map(v => {
                const opt = question.options.find(o => o.value === v)
                return opt?.label || v
              })
              displayValue = labels.join('、')
            } else {
              const option = question.options.find(o => o.value === value)
              displayValue = option?.label || value
            }
            
            return (
              <div key={key} className="flex justify-between gap-2">
                <span className="text-slate-500 dark:text-slate-400 text-xs">{question.title}</span>
                <span className="font-medium text-slate-800 dark:text-slate-100 text-xs text-right truncate max-w-[60%]">{displayValue}</span>
              </div>
            )
          })}
        </div>
      </div>

      {/* 风险提示 */}
      <div className="bg-amber-50 dark:bg-amber-900/20 border border-amber-200 dark:border-amber-800 rounded-xl p-3">
        <p className="text-xs text-amber-800 dark:text-amber-200">
          ⚠️ 风险提示：以上投资风格建议仅供参考，实际投资请根据市场情况和个人风险承受能力谨慎决策。本系统仅支持A股市场投资建议。
        </p>
      </div>

      {/* 操作按钮 */}
      <div className="space-y-3">
        <button
          onClick={onConfirm}
          disabled={isLoading}
          className="w-full py-4 bg-gradient-to-r from-brand-600 to-purple-600 text-white rounded-xl font-medium hover:opacity-90 disabled:opacity-50 transition-opacity flex items-center justify-center gap-2"
        >
          {isLoading ? (
            <>
              <div className="w-4 h-4 border-2 border-white border-t-transparent rounded-full animate-spin" />
              生成A股投资方案中...
            </>
          ) : (
            <>
              <Sparkles className="w-5 h-5" />
              基于此风格生成A股投资方案
            </>
          )}
        </button>
        <button
          onClick={onEdit}
          className="w-full py-3 text-slate-600 dark:text-slate-400 hover:text-slate-800 dark:hover:text-slate-200 text-sm"
        >
          ← 修改选择
        </button>
      </div>
    </div>
  )
}

// ==================== 规划中组件 ====================

function PlanningScreen() {
  const { progress } = usePlannerStore()
  const steps = [
    { label: '分析A股市场环境', done: true },
    { label: '匹配A股投资策略', done: true },
    { label: '计算预期收益与风险', done: true },
    { label: 'A股行业风险评估', done: true },
    { label: '生成最终投资方案', done: true },
  ]

  return (
    <div className="flex flex-col items-center justify-center py-12">
      <div className="w-16 h-16 rounded-2xl bg-brand-100 dark:bg-brand-900/30 flex items-center justify-center mb-6">
        <div className="w-8 h-8 border-3 border-brand-600 border-t-transparent rounded-full animate-spin" />
      </div>
      <h3 className="text-lg font-medium text-slate-800 dark:text-slate-100 mb-4">
        正在为您生成A股投资方案...
      </h3>
      {progress ? (
        <p className="text-sm text-slate-600 dark:text-slate-400 mb-6 max-w-sm text-center break-words">
          {progress}
        </p>
      ) : (
        <p className="text-sm text-slate-600 dark:text-slate-400 mb-6">
          正在计算并构建投资组合，请稍候…
        </p>
      )}
      <div className="w-full max-w-sm">
        {steps.map((step, i) => (
          <div key={i} className="flex items-center gap-3 mb-3">
            <CheckCircle className="w-5 h-5 text-green-500" />
            <span className="text-sm text-slate-500 dark:text-slate-400">{step.label}</span>
            <div className="flex-1 h-1 bg-slate-200 dark:bg-slate-700 rounded-full overflow-hidden">
              <div className="h-full bg-brand-600 animate-pulse" />
            </div>
          </div>
        ))}
      </div>
    </div>
  )
}

// ==================== 方案审核组件 ====================

function PlanReviewScreen({
  plan,
  candidates,
  onApprove,
  generateStockPool,
  isLoading,
}: {
  plan: any
  candidates: CandidateStrategy[]
  onApprove: (c: CandidateStrategy) => void
  generateStockPool: () => void
  isLoading: boolean
}) {
  const mandate = plan.mandate
  const construction = plan.construction
  const riskLabels: Record<string, string> = {
    conservative: '保守型',
    balanced: '平衡型',
    growth: '积极型',
  }

  if (!mandate || !construction) {
    return (
      <div className="text-center py-12">
        <div className="w-16 h-16 rounded-2xl bg-brand-100 flex items-center justify-center mx-auto mb-4">
          <div className="w-8 h-8 border-3 border-brand-600 border-t-transparent rounded-full animate-spin" />
        </div>
        <p className="text-slate-600">正在生成投资方案，请稍后...</p>
      </div>
    )
  }

  return (
    <div className="space-y-8">
      {/* ===== 1. 投资任务书（Investment Mandate） ===== */}
      <div className="bg-gradient-to-br from-brand-50 to-purple-50 dark:from-brand-900/20 dark:to-purple-900/20 rounded-2xl p-6">
        <div className="flex items-center gap-2 mb-4">
          <div className="w-8 h-8 rounded-lg bg-white/80 dark:bg-slate-800 flex items-center justify-center">
            <FileText className="w-4 h-4 text-brand-600" />
          </div>
          <div>
            <h3 className="text-lg font-bold text-slate-800 dark:text-slate-100">投资任务书（Investment Mandate）</h3>
            <p className="text-xs text-slate-500">CIO 管理您账户必须遵守的投资宪法 · 版本 {mandate.version}</p>
          </div>
        </div>

        <div className="bg-white dark:bg-slate-800 rounded-xl p-4 space-y-4">
          {/* 核心目标 4 个数字 */}
          <div className="grid grid-cols-2 md:grid-cols-4 gap-3">
            <MandateMetric
              label="目标收益区间"
              value={`${(mandate.targetReturnRange[0] * 100).toFixed(0)}–${(mandate.targetReturnRange[1] * 100).toFixed(0)}%`}
              icon={<TrendingUp className="w-4 h-4" />}
              color="text-green-600"
            />
            <MandateMetric
              label="目标波动率"
              value={`≤${(mandate.targetVolatility * 100).toFixed(0)}%`}
              icon={<BarChart3 className="w-4 h-4" />}
              color="text-blue-600"
            />
            <MandateMetric
              label="最大回撤"
              value={`≤${(mandate.maxDrawdown * 100).toFixed(0)}%`}
              icon={<Shield className="w-4 h-4" />}
              color="text-orange-600"
            />
            <MandateMetric
              label="预计持仓数"
              value={`${construction.positions.length} 支`}
              icon={<PieChart className="w-4 h-4" />}
              color="text-purple-600"
            />
          </div>

          {/* 画像摘要 */}
          <div className="border-t border-slate-200 dark:border-slate-700 pt-3">
            <h4 className="text-xs font-medium text-slate-500 mb-2">投资者画像</h4>
            <div className="grid grid-cols-2 md:grid-cols-3 gap-2 text-xs">
              {Object.entries(mandate.investorProfile).map(([k, v]) => (
                <div key={k} className="flex justify-between bg-slate-50 dark:bg-slate-900/50 rounded px-2 py-1">
                  <span className="text-slate-500">{profileLabelMap[k] || k}</span>
                  <span className="font-medium text-slate-800 dark:text-slate-200">{String(v)}</span>
                </div>
              ))}
            </div>
          </div>

          {/* 风险预算 */}
          <div className="border-t border-slate-200 dark:border-slate-700 pt-3">
            <h4 className="text-xs font-medium text-slate-500 mb-2">风险预算（Risk Budget）</h4>
            <div className="grid grid-cols-2 md:grid-cols-4 gap-2 text-xs">
              <BudgetItem label="单股最大" value={`${(mandate.positionLimit.maxSingleStock * 100).toFixed(0)}%`} />
              <BudgetItem label="高波动上限" value={`${(mandate.positionLimit.highVolLimit * 100).toFixed(0)}%`} />
              <BudgetItem label="现金要求" value={`${(mandate.cashRequirement[0] * 100).toFixed(0)}–${(mandate.cashRequirement[1] * 100).toFixed(0)}%`} />
              <BudgetItem label="最大杠杆" value={`${(mandate.leverageLimit * 100).toFixed(0)}%`} />
            </div>
          </div>

          {/* 风格与约束 */}
          <div className="border-t border-slate-200 dark:border-slate-700 pt-3">
            <h4 className="text-xs font-medium text-slate-500 mb-2">
              风格偏好 · {mandate.stylePreferences.primaryStyle}
              <span className="ml-2 text-slate-400">· 再平衡: {mandate.rebalancePolicy.frequency}</span>
            </h4>
            <div className="flex flex-wrap gap-1.5">
              {(mandate.stylePreferences.preferredIndustries as string[]).map((ind: string) => (
                <span key={ind} className="px-2 py-0.5 bg-brand-100 dark:bg-brand-900/40 text-brand-700 dark:text-brand-300 text-xs rounded-full">
                  {ind}
                </span>
              ))}
            </div>
          </div>
        </div>
      </div>

      {/* ===== 2. 组合构建过程 ===== */}
      <div className="bg-white dark:bg-slate-800 rounded-2xl p-6 border border-slate-200 dark:border-slate-700">
        <div className="flex items-center gap-2 mb-4">
          <Activity className="w-5 h-5 text-brand-600" />
          <h3 className="text-lg font-bold text-slate-800 dark:text-slate-100">组合构建过程</h3>
        </div>

        <div className="space-y-3">
          {(construction.processSteps as any[]).map((step: any) => (
            <div key={step.stepNo} className="flex gap-3">
              <div className="flex flex-col items-center">
                <div className={`w-8 h-8 rounded-full flex items-center justify-center text-xs font-bold ${
                  step.status === 'DONE' ? 'bg-green-500 text-white' :
                  step.status === 'RUNNING' ? 'bg-brand-500 text-white animate-pulse' :
                  'bg-slate-200 dark:bg-slate-700 text-slate-500'
                }`}>
                  {step.status === 'DONE' ? '✓' : step.stepNo}
                </div>
                {step.stepNo < construction.processSteps.length && (
                  <div className="w-px flex-1 bg-slate-200 dark:bg-slate-700 min-h-[20px]" />
                )}
              </div>
              <div className="flex-1 pb-3">
                <div className="flex items-center gap-2 mb-0.5">
                  <span className="font-medium text-slate-800 dark:text-slate-100 text-sm">{step.title}</span>
                  <span className="px-1.5 py-0.5 bg-slate-100 dark:bg-slate-700 text-xs text-slate-500 rounded">
                    {step.agentRole}
                  </span>
                </div>
                <p className="text-sm text-slate-600 dark:text-slate-400">{step.message}</p>
                <p className="text-xs text-slate-500 mt-0.5">{step.detail}</p>
              </div>
            </div>
          ))}
        </div>
      </div>

      {/* ===== 3. AI 投资方案摘要 ===== */}
      <div className="bg-gradient-to-br from-green-50 to-emerald-50 dark:from-green-900/20 dark:to-emerald-900/20 rounded-2xl p-6">
        <h3 className="text-lg font-bold text-slate-800 dark:text-slate-100 mb-3">AI 为你制定的A股投资方案</h3>
        <p className="text-sm text-slate-600 dark:text-slate-400 mb-4">
          根据你的资金规模、投资目标和风险承受能力，AI投资团队为你制定了一套{riskLabels[mandate.riskLevel]}型A股投资方案。
        </p>

        <div className="grid grid-cols-2 md:grid-cols-4 gap-3">
          <MetricCard title="目标收益" value={`${(construction.expectedReturn * 100).toFixed(1)}%`} hint="年化目标" />
          <MetricCard title="目标波动" value={`≤${(construction.expectedVolatility * 100).toFixed(0)}%`} hint="年化标准差" />
          <MetricCard title="最大回撤" value={`≤${(construction.maxDrawdown * 100).toFixed(0)}%`} hint="历史模拟范围" />
          <MetricCard title="夏普比率" value={construction.sharpeRatio.toFixed(2)} hint="风险调整收益" />
        </div>

        {/* AI 讲解（由大模型生成） */}
        {(plan as any).llmInsight && (
          <div className="mt-4 bg-white dark:bg-slate-800 rounded-xl p-4">
            <div className="flex items-center gap-2 mb-3">
              <span className="w-2 h-2 rounded-full bg-emerald-500 animate-pulse" />
              <h4 className="text-sm font-semibold text-slate-700 dark:text-slate-300">AI 投资团队讲解</h4>
            </div>
            {(plan as any).llmInsight.mandateInsight && (
              <p className="text-sm text-slate-600 dark:text-slate-400 leading-relaxed">{(plan as any).llmInsight.mandateInsight}</p>
            )}
            {(plan as any).llmInsight.strategyRationale && (
              <div className="mt-3">
                <div className="text-xs font-medium text-slate-500 dark:text-slate-400 mb-1">策略思路</div>
                <p className="text-sm text-slate-600 dark:text-slate-400">{(plan as any).llmInsight.strategyRationale}</p>
              </div>
            )}
            {((plan as any).llmInsight.riskConsiderations?.length > 0) && (
              <div className="mt-3">
                <div className="text-xs font-medium text-amber-600 dark:text-amber-400 mb-1">风险提示</div>
                <ul className="list-disc list-inside space-y-1">
                  {((plan as any).llmInsight.riskConsiderations as string[]).map((r: string, i: number) => (
                    <li key={i} className="text-sm text-slate-500 dark:text-slate-400">{r}</li>
                  ))}
                </ul>
              </div>
            )}
            {((plan as any).llmInsight.recommendations?.length > 0) && (
              <div className="mt-3">
                <div className="text-xs font-medium text-emerald-600 dark:text-emerald-400 mb-1">关键建议</div>
                <ul className="list-disc list-inside space-y-1">
                  {((plan as any).llmInsight.recommendations as string[]).map((r: string, i: number) => (
                    <li key={i} className="text-sm text-slate-600 dark:text-slate-400">{r}</li>
                  ))}
                </ul>
              </div>
            )}
          </div>
        )}

        {/* 资产配置 */}
        <div className="mt-5 bg-white dark:bg-slate-800 rounded-xl p-4">
          <h4 className="text-sm font-medium text-slate-700 dark:text-slate-300 mb-3">资产配置</h4>
          <div className="flex h-6 rounded-lg overflow-hidden mb-3">
            <div className="bg-blue-500 flex items-center justify-center text-xs text-white font-medium" style={{width: `${construction.equityWeight * 100}%`}}>
              {construction.equityWeight > 0.03 && <>股票 {(construction.equityWeight * 100).toFixed(0)}%</>}
            </div>
            <div className="bg-purple-500 flex items-center justify-center text-xs text-white font-medium" style={{width: `${construction.fundWeight * 100}%`}}>
              {construction.fundWeight > 0.03 && <>基金 {(construction.fundWeight * 100).toFixed(0)}%</>}
            </div>
            <div className="bg-slate-400 flex items-center justify-center text-xs text-white font-medium" style={{width: `${construction.cashWeight * 100}%`}}>
              {construction.cashWeight > 0.03 && <>现金 {(construction.cashWeight * 100).toFixed(0)}%</>}
            </div>
          </div>
          <div className="flex flex-wrap gap-3 text-xs">
            <span>资金规模: ¥{construction.totalAssets.toLocaleString('zh-CN')}</span>
          </div>
        </div>
      </div>

      {/* ===== 4. 投资组合明细 ===== */}
      <div className="bg-white dark:bg-slate-800 rounded-2xl p-6 border border-slate-200 dark:border-slate-700">
        <div className="flex items-center justify-between mb-4">
          <h3 className="text-lg font-bold text-slate-800 dark:text-slate-100">AI 投资组合（建议持仓）</h3>
          <span className="text-xs text-slate-500">{construction.positions.length} 个持仓</span>
        </div>

        <div className="overflow-hidden rounded-xl border border-slate-200 dark:border-slate-700">
          <table className="w-full text-sm">
            <thead className="bg-slate-50 dark:bg-slate-900/50 text-xs text-slate-500">
              <tr>
                <th className="text-left px-3 py-2">资产</th>
                <th className="text-left px-3 py-2">行业</th>
                <th className="text-right px-3 py-2">权重</th>
                <th className="text-right px-3 py-2">建议金额</th>
                <th className="text-right px-3 py-2">股价/手数</th>
                <th className="text-center px-3 py-2">可交易</th>
              </tr>
            </thead>
            <tbody>
              {(construction.positions as any[]).map((pos: any) => (
                <tr key={pos.assetCode} className="border-t border-slate-100 dark:border-slate-700 hover:bg-slate-50 dark:hover:bg-slate-900/30">
                  <td className="px-3 py-2">
                    <div className="font-medium text-slate-800 dark:text-slate-100">{pos.assetName}</div>
                    <div className="text-xs text-slate-500">{pos.assetCode} · {pos.assetType}</div>
                  </td>
                  <td className="px-3 py-2 text-slate-600 dark:text-slate-400 text-xs">{pos.industry}</td>
                  <td className="px-3 py-2 text-right font-medium text-slate-800 dark:text-slate-100">
                    {(pos.targetWeight * 100).toFixed(1)}%
                  </td>
                  <td className="px-3 py-2 text-right text-slate-600 dark:text-slate-400">
                    ¥{pos.suggestedAmount.toLocaleString('zh-CN', {maximumFractionDigits: 0})}
                  </td>
                  <td className="px-3 py-2 text-right text-xs">
                    {pos.stockPrice ? (
                      <>
                        <div className="text-slate-700 dark:text-slate-300">¥{pos.stockPrice.toFixed(2)}</div>
                        <div className={pos.tradeable ? 'text-green-600 dark:text-green-400' : 'text-red-500'}>
                          {pos.lots}手 ({pos.lotSize}股)
                        </div>
                      </>
                    ) : (
                      <span className="text-slate-400">—</span>
                    )}
                  </td>
                  <td className="px-3 py-2 text-center">
                    {pos.tradeable === false ? (
                      <span className="inline-flex items-center gap-1 px-1.5 py-0.5 rounded bg-red-50 dark:bg-red-900/30 text-red-700 dark:text-red-400 text-xs">
                        ✗ 不可交易
                      </span>
                    ) : pos.assetType === 'CASH' ? (
                      <span className="inline-flex items-center gap-1 px-1.5 py-0.5 rounded bg-slate-100 dark:bg-slate-700 text-slate-600 dark:text-slate-400 text-xs">
                        现金
                      </span>
                    ) : (
                      <span className="inline-flex items-center gap-1 px-1.5 py-0.5 rounded bg-green-50 dark:bg-green-900/30 text-green-700 dark:text-green-400 text-xs">
                        ✓ 可交易
                      </span>
                    )}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>

        {/* 持仓详情展开区（首个持仓示例） */}
        {construction.positions.length > 0 && (
          <div className="mt-4 bg-slate-50 dark:bg-slate-900/50 rounded-xl p-4">
            <h4 className="text-sm font-medium text-slate-700 dark:text-slate-300 mb-2">💡 AI 决策解释（首个持仓）</h4>
            {(() => {
              const top = construction.positions[0]
              return (
                <div className="text-sm text-slate-600 dark:text-slate-400 space-y-1">
                  <p>
                    <span className="font-medium text-slate-800 dark:text-slate-200">{top.assetName}</span>
                    <span className="ml-2">— {top.rationale}</span>
                  </p>
                  {top.stockPrice && (
                    <p className="text-xs text-slate-500">
                      股价 ¥{top.stockPrice.toFixed(2)} · 一手成本 ¥{(top.stockPrice * top.lotSize).toFixed(0)} · 
                      建议买入 {top.lots} 手 ({top.lots * top.lotSize} 股) · 
                      <span className={top.tradeable ? 'text-green-600' : 'text-red-500'}>
                        {top.tradeable ? ' ✓ 可交易' : ' ✗ 资金不足'}
                      </span>
                    </p>
                  )}
                  <div className="text-xs text-slate-500 mt-2">
                    量化证据：
                    {(Object.entries(top.quantEvidence) as [string, number][]).map(([k, v]) => (
                      <span key={k} className="inline-block mx-1.5">
                        {quantLabelMap[k] || k}: <span className="font-medium">{(v as number).toFixed(2)}</span>
                      </span>
                    ))}
                  </div>
                  <p className="text-xs text-slate-500 mt-1">
                    三层权重：基础 {(top.baseWeight * 100).toFixed(1)}% × Alpha {top.alphaAdjust.toFixed(2)} × 风险 {top.riskAdjust.toFixed(2)} = 最终 {(top.targetWeight * 100).toFixed(1)}%
                  </p>
                </div>
              )
            })()}
          </div>
        )}
      </div>

      {/* ===== 5. 候选策略对比（保留参考） ===== */}
      {candidates.length > 0 && (
        <div className="bg-white dark:bg-slate-800 rounded-2xl p-6 border border-slate-200 dark:border-slate-700">
          <h3 className="text-base font-bold text-slate-800 dark:text-slate-100 mb-3">
            三种候选策略对比（供 CIO 参考）
          </h3>
          <div className="grid grid-cols-1 md:grid-cols-3 gap-3">
            {candidates.map((candidate) => (
              <div key={candidate.id} className="bg-slate-50 dark:bg-slate-900/30 rounded-lg p-3">
                <div className="flex items-center gap-2 mb-2">
                  <span className="text-sm font-bold text-brand-600">{candidate.label}</span>
                  <span className="font-medium text-slate-800 dark:text-slate-100 text-sm">{candidate.name}</span>
                </div>
                <div className="text-xs text-slate-500 line-clamp-3 mb-2">{candidate.description}</div>
                <div className="grid grid-cols-3 gap-1 text-xs">
                  <div className="text-center">
                    <div className="font-medium text-slate-800 dark:text-slate-100">{(candidate.expectedReturn * 100).toFixed(1)}%</div>
                    <div className="text-slate-400">收益</div>
                  </div>
                  <div className="text-center">
                    <div className="font-medium text-slate-800 dark:text-slate-100">{(candidate.maxDrawdown * 100).toFixed(1)}%</div>
                    <div className="text-slate-400">回撤</div>
                  </div>
                  <div className="text-center">
                    <div className="font-medium text-slate-800 dark:text-slate-100">{candidate.sharpe.toFixed(2)}</div>
                    <div className="text-slate-400">夏普</div>
                  </div>
                </div>
              </div>
            ))}
          </div>
        </div>
      )}

      {/* ===== 6. 投资规划自动生成股票池 ===== */}
      <div className="bg-white dark:bg-slate-800 rounded-2xl p-6 border border-slate-200 dark:border-slate-700">
        <div className="flex items-center justify-between gap-2 mb-3">
          <div className="flex items-center gap-2">
            <BarChart3 className="w-5 h-5 text-brand-500" />
            <h3 className="text-base font-bold text-slate-800 dark:text-slate-100">
              智能选股股票池（供智能体选用）
            </h3>
          </div>
          <button
            onClick={() => generateStockPool()}
            disabled={isLoading}
            className="inline-flex items-center gap-1.5 px-3 py-1.5 rounded-lg text-sm font-medium text-white bg-brand-600 hover:bg-brand-700 disabled:opacity-50 transition-colors"
          >
            {plan.stockPool ? <RotateCcw className="w-4 h-4" /> : <Sparkles className="w-4 h-4" />}
            {plan.stockPool ? '重新生成股票池' : '生成股票池'}
          </button>
        </div>

        {plan.stockPool ? (
          <>
            <div className="grid grid-cols-2 md:grid-cols-4 gap-3">
              <div className="bg-slate-50 dark:bg-slate-900/40 rounded-lg p-3">
                <div className="text-xs text-slate-500 mb-1">策略模板</div>
                <div className="text-lg font-bold text-slate-800 dark:text-slate-100">
                  {({ balanced: '均衡配置', defensive: '防御型', growth: '成长型' } as Record<string, string>)[plan.stockPool.strategyId] || plan.stockPool.strategyId}
                </div>
              </div>
              <div className="bg-slate-50 dark:bg-slate-900/40 rounded-lg p-3">
                <div className="text-xs text-slate-500 mb-1">已纳入股票池</div>
                <div className="text-lg font-bold text-brand-600">{plan.stockPool.submittedCount} 只</div>
              </div>
              <div className="bg-slate-50 dark:bg-slate-900/40 rounded-lg p-3">
                <div className="text-xs text-slate-500 mb-1">引擎筛选结果</div>
                <div className="text-lg font-bold text-slate-800 dark:text-slate-100">{plan.stockPool.totalResults} 只</div>
              </div>
              <div className="bg-slate-50 dark:bg-slate-900/40 rounded-lg p-3">
                <div className="text-xs text-slate-500 mb-1">生成时间</div>
                <div className="text-sm font-medium text-slate-700 dark:text-slate-300">
                  {plan.stockPool.generatedAt ? new Date(plan.stockPool.generatedAt).toLocaleString('zh-CN') : '—'}
                </div>
              </div>
            </div>
            {plan.stockPool.profileSummary && (
              <div className="mt-3 text-xs text-slate-500 dark:text-slate-400 bg-slate-50 dark:bg-slate-900/40 rounded-lg p-3">
                画像摘要：{plan.stockPool.profileSummary}
              </div>
            )}
          </>
        ) : (
          <div className="text-sm text-slate-500 dark:text-slate-400 bg-slate-50 dark:bg-slate-900/40 rounded-lg p-4">
            尚未生成股票池。点击右上角「生成股票池」，系统将根据本投资计划自动调用智能选股引擎，将符合条件的候选股票纳入可交易股票池，供 AI 智能体选用。
          </div>
        )}
        <p className="mt-3 text-xs text-slate-400 dark:text-slate-500">
          系统根据投资计划（风险等级）匹配选股策略模板，自动筛选并纳入可交易股票池，AI 智能体可直接选用。
        </p>
      </div>

      {/* 风险提示 & CIO 审核状态 */}
      <div className="bg-amber-50 dark:bg-amber-900/20 border border-amber-200 dark:border-amber-800 rounded-xl p-4">
        <p className="text-sm text-amber-800 dark:text-amber-200">
          ⚠️ 风险提示：A股市场有风险，投资需谨慎。以上方案基于历史数据和量化模型生成，仅供参考，不构成投资建议。
        </p>
      </div>

      {/* CIO 审核状态提示 */}
      {plan.status === 'REVIEWING' ? (
        <div className="bg-blue-50 dark:bg-blue-900/20 border border-blue-200 dark:border-blue-800 rounded-xl p-4">
          <div className="flex items-center gap-3">
            <div className="w-6 h-6 border-2 border-blue-500 border-t-transparent rounded-full animate-spin" />
            <div>
              <p className="text-sm font-medium text-blue-800 dark:text-blue-200">AI CIO 正在审核此投资方案</p>
              <p className="text-xs text-blue-600 dark:text-blue-300 mt-1">
                首席投资官将根据您的风险承受能力和投资目标，自动分析方案的合理性并做出批准或拒绝决定。
              </p>
            </div>
          </div>
        </div>
      ) : plan.status === 'ACTIVE' || plan.status === 'APPROVED' ? (
        <div className="bg-green-50 dark:bg-green-900/20 border border-green-200 dark:border-green-800 rounded-xl p-4">
          <div className="flex items-center gap-2">
            <CheckCircle className="w-5 h-5 text-green-500" />
            <p className="text-sm font-medium text-green-800 dark:text-green-200">CIO 已批准此投资方案，已自动生效</p>
          </div>
        </div>
      ) : plan.status === 'REJECTED' ? (
        <div className="bg-red-50 dark:bg-red-900/20 border border-red-200 dark:border-red-800 rounded-xl p-4">
          <div className="flex items-center gap-2">
            <AlertTriangle className="w-5 h-5 text-red-500" />
            <div>
              <p className="text-sm font-medium text-red-800 dark:text-red-200">此投资方案已被 CIO 拒绝</p>
              <p className="text-xs text-red-600 dark:text-red-300 mt-1">
                您可以重新生成新的投资方案，或调整投资目标和风险承受能力后再次尝试。
              </p>
            </div>
          </div>
        </div>
      ) : (
        <button
          onClick={() => onApprove(candidates[1] || candidates[0])}
          disabled={isLoading}
          className="w-full py-3 bg-gradient-to-r from-brand-600 to-purple-600 text-white rounded-xl font-medium hover:opacity-90 transition-opacity flex items-center justify-center gap-2 disabled:opacity-50 disabled:cursor-not-allowed"
        >
          {isLoading ? (
            <>
              <div className="w-4 h-4 border-2 border-white border-t-transparent rounded-full animate-spin" />
              处理中...
            </>
          ) : (
            <>
              <CheckCircle className="w-4 h-4" />
              确认并采纳此投资方案
            </>
          )}
        </button>
      )}
    </div>
  )
}

// ===== 辅助小组件 =====
const profileLabelMap: Record<string, string> = {
  capital: '资金规模',
  investment_horizon: '投资期限',
  investment_objective: '投资目标',
  risk_tolerance: '风险承受',
  investment_style: '风格偏好',
  experience: '投资经验',
}

const quantLabelMap: Record<string, string> = {
  alpha_score: 'Alpha',
  risk_score: '风险',
  liquidity_score: '流动性',
  factor_score: '因子',
}

function MandateMetric({ label, value, icon, color }: { label: string; value: string; icon: ReactNode; color: string }) {
  return (
    <div className="bg-slate-50 dark:bg-slate-900/40 rounded-lg p-3">
      <div className={`flex items-center gap-1 text-xs ${color} mb-1`}>{icon}{label}</div>
      <div className="text-lg font-bold text-slate-800 dark:text-slate-100">{value}</div>
    </div>
  )
}

function BudgetItem({ label, value }: { label: string; value: string }) {
  return (
    <div className="bg-slate-50 dark:bg-slate-900/50 rounded px-2 py-1.5">
      <div className="text-slate-500">{label}</div>
      <div className="font-medium text-slate-800 dark:text-slate-100">{value}</div>
    </div>
  )
}

function MetricCard({ title, value, hint }: { title: string; value: string; hint: string }) {
  return (
    <div className="bg-white dark:bg-slate-800 rounded-xl p-3">
      <div className="text-xs text-slate-500 mb-1">{title}</div>
      <div className="text-xl font-bold text-slate-800 dark:text-slate-100">{value}</div>
      <div className="text-xs text-slate-400 mt-0.5">{hint}</div>
    </div>
  )
}

// ==================== 完成组件 ====================

function CompleteScreen({ plan }: { plan: any }) {
  const mandate = plan.mandate
  const construction = plan.construction

  return (
    <div className="py-6">
      <div className="flex flex-col items-center justify-center text-center mb-6">
        <div className="w-20 h-20 rounded-full bg-green-100 dark:bg-green-900/30 flex items-center justify-center mb-4">
          <CheckCircle className="w-10 h-10 text-green-600" />
        </div>
        <h3 className="text-2xl font-bold text-slate-800 dark:text-slate-100 mb-2">规划完成！</h3>
        <p className="text-slate-600 dark:text-slate-400 mb-1">您的A股投资方案已生成</p>
        <p className="text-sm text-slate-500 dark:text-slate-500">{plan.name}</p>
      </div>

      {mandate && construction && (
        <div className="max-w-2xl mx-auto bg-white dark:bg-slate-800 rounded-2xl p-6 border border-slate-200 dark:border-slate-700">
          <h4 className="text-sm font-bold text-slate-800 dark:text-slate-100 mb-3">📋 你的投资任务书摘要</h4>
          <div className="grid grid-cols-2 md:grid-cols-4 gap-3 mb-4">
            <div className="bg-slate-50 dark:bg-slate-900/50 rounded-lg p-3 text-center">
              <div className="text-xs text-slate-500">目标收益</div>
              <div className="text-base font-bold text-green-600">
                {(mandate.targetReturnRange[0] * 100).toFixed(0)}–{(mandate.targetReturnRange[1] * 100).toFixed(0)}%
              </div>
            </div>
            <div className="bg-slate-50 dark:bg-slate-900/50 rounded-lg p-3 text-center">
              <div className="text-xs text-slate-500">最大回撤</div>
              <div className="text-base font-bold text-orange-600">≤{(mandate.maxDrawdown * 100).toFixed(0)}%</div>
            </div>
            <div className="bg-slate-50 dark:bg-slate-900/50 rounded-lg p-3 text-center">
              <div className="text-xs text-slate-500">持仓数</div>
              <div className="text-base font-bold text-brand-600">{construction.positions.length} 支</div>
            </div>
            <div className="bg-slate-50 dark:bg-slate-900/50 rounded-lg p-3 text-center">
              <div className="text-xs text-slate-500">夏普比率</div>
              <div className="text-base font-bold text-purple-600">{construction.sharpeRatio.toFixed(2)}</div>
            </div>
          </div>

          <div className="border-t border-slate-200 dark:border-slate-700 pt-3">
            <h5 className="text-xs font-medium text-slate-500 mb-2">资产配置</h5>
            <div className="flex h-4 rounded-lg overflow-hidden mb-2">
              <div className="bg-blue-500" style={{width: `${construction.equityWeight * 100}%`}} />
              <div className="bg-purple-500" style={{width: `${construction.fundWeight * 100}%`}} />
              <div className="bg-slate-400" style={{width: `${construction.cashWeight * 100}%`}} />
            </div>
            <div className="flex justify-between text-xs text-slate-500">
              <span>股票 {(construction.equityWeight * 100).toFixed(0)}%</span>
              <span>基金 {(construction.fundWeight * 100).toFixed(0)}%</span>
              <span>现金 {(construction.cashWeight * 100).toFixed(0)}%</span>
            </div>
          </div>

          <div className="mt-4 bg-amber-50 dark:bg-amber-900/20 border border-amber-200 dark:border-amber-800 rounded-lg p-3">
            <p className="text-xs text-amber-800 dark:text-amber-200">
              ⚠️ 本方案仅供参考，不构成投资建议。A股市场有风险，投资需谨慎。
            </p>
          </div>
        </div>
      )}
      
      <div className="w-full max-w-sm mx-auto space-y-4 mt-6">
        <p className="text-xs text-slate-500 dark:text-slate-500 text-center">
          点右上角的“重置”按钮进行新的规划
        </p>
      </div>
    </div>
  )
}