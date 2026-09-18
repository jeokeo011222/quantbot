package policy

import (
	"encoding/json"
	"fmt"
	"log"
	"math"
	"time"

	"github.com/quantpilot/quantpilot/internal/data"
	"github.com/quantpilot/quantpilot/internal/port"
	"github.com/quantpilot/quantpilot/internal/util"
)

// PolicyCheckResult Policy检查结果
type PolicyCheckResult struct {
	Passed     bool     `json:"passed"`
	Decision   string   `json:"decision"`
	Reason     string   `json:"reason"`
	Violations []string `json:"violations"`
}

// PolicyEngine 确定性安全层 - 不允许任何LLM修改硬规则
type PolicyEngine struct {
	db            *data.SQLiteManager
	emergencyStop bool
	// pauseBuilding 强制暂停建仓开关：置位时禁止一切买入成交，但卖出（离场/止损）不受影响。
	// 由 CIO "暂停建仓" 指令置位，作为买入执行路径的共享阻断闸门（最终执行层防线，
	// 覆盖 CIO 建仓 / 自动股票池 / place_trade / 审批补确认 所有买入入口）。
	pauseBuilding  bool
	hardLimits     HardLimits
	sectorProvider SectorProvider
}

// SectorProvider 提供"股票代码 -> 行业"映射，用于行业暴露校验。
// 由外部注入（生产环境用 data.DictLoader.GetIndustryByStock），便于测试替换。
type SectorProvider interface {
	SectorOf(code string) string
}

// dictSectorProvider 基于 data.DictLoader 的行业提供者（默认实现）
type dictSectorProvider struct{}

// SectorOf 返回股票所属行业名称（未知标的由 GetIndustryByStock 兜底为"通用"）
func (dictSectorProvider) SectorOf(code string) string {
	return data.GetDictLoader().GetIndustryByStock(code)
}

// HardLimits 硬风险限制
type HardLimits struct {
	MaxSinglePosition    float64
	MaxSectorExposure    float64
	MaxPortfolioLeverage float64
	MaxDailyLoss         float64
	MaxDrawdown          float64
	MaxOrderSize         float64
	MaxTurnover          float64
	MaxSlippage          float64
	MinLiquidity         float64
}

// NewPolicyEngine 创建Policy Engine
func NewPolicyEngine(db *data.SQLiteManager) *PolicyEngine {
	return &PolicyEngine{
		db:            db,
		emergencyStop: false,
		// 行业提供者默认使用 DictLoader；可通过 SetSectorProvider 替换注入
		sectorProvider: dictSectorProvider{},
		hardLimits: HardLimits{
			MaxSinglePosition:    0.30,
			MaxSectorExposure:    0.50,
			MaxPortfolioLeverage: 1.50,
			MaxDailyLoss:         0.05,
			MaxDrawdown:          0.10,
			MaxOrderSize:         100000,
			MaxTurnover:          1.00,
			MaxSlippage:          0.005,
			MinLiquidity:         0.10,
		},
	}
}

// SetEmergencyStop 设置紧急停止
func (pe *PolicyEngine) SetEmergencyStop(stop bool) {
	pe.emergencyStop = stop
	if stop {
		log.Println("[Policy] EMERGENCY STOP activated - all trading halted")
		pe.writePolicyLog("emergency_stop", map[string]interface{}{
			"status": "activated",
		})
	} else {
		log.Println("[Policy] EMERGENCY STOP deactivated")
		pe.writePolicyLog("emergency_stop", map[string]interface{}{
			"status": "deactivated",
		})
	}
}

// IsEmergencyStopped 检查是否紧急停止
func (pe *PolicyEngine) IsEmergencyStopped() bool {
	return pe.emergencyStop
}

// SetPauseBuilding 设置强制暂停建仓开关。置位后一切买入被共享闸门阻断，但卖出不受影响。
func (pe *PolicyEngine) SetPauseBuilding(pause bool) {
	pe.pauseBuilding = pause
	if pause {
		log.Println("[Policy] PAUSE BUILDING activated - all buy orders blocked, sells remain allowed")
		pe.writePolicyLog("pause_building", map[string]interface{}{
			"status": "activated",
		})
	} else {
		log.Println("[Policy] PAUSE BUILDING deactivated")
		pe.writePolicyLog("pause_building", map[string]interface{}{
			"status": "deactivated",
		})
	}
}

// IsPauseBuilding 检查是否处于强制暂停建仓状态。
func (pe *PolicyEngine) IsPauseBuilding() bool {
	return pe.pauseBuilding
}

// SetSectorProvider 注入/替换行业提供者（生产默认用 DictLoader，可按需覆盖）
func (pe *PolicyEngine) SetSectorProvider(p SectorProvider) {
	if p == nil {
		pe.sectorProvider = dictSectorProvider{}
		return
	}
	pe.sectorProvider = p
}

// sectorOf 解析订单/持仓代码所属行业；未注入提供者时返回空串（跳过行业校验）
func (pe *PolicyEngine) sectorOf(code string) string {
	if pe.sectorProvider == nil {
		return ""
	}
	if s := pe.sectorProvider.SectorOf(code); s != "" {
		return s
	}
	return ""
}

// CheckOrderIntent 检查订单意图是否符合Policy。
// 覆盖现金/可用资金、单票仓位（含既有持仓）、行业暴露、组合杠杆与输入健壮性校验。
// 行业暴露由 SectorProvider 注入的"股票代码->行业"映射提供数据；
// MaxTurnover / MaxSlippage / MinLiquidity 依赖成交量等外部执行数据，当前未注入时无法校验。
func (pe *PolicyEngine) CheckOrderIntent(intent port.OrderIntent, portfolioValue float64, currentPositions map[string]float64) PolicyCheckResult {
	if pe.emergencyStop {
		return PolicyCheckResult{
			Passed:     false,
			Decision:   "BLOCKED",
			Reason:     "EMERGENCY_STOP - Trading halted",
			Violations: []string{"emergency_stop_active"},
		}
	}

	violations := []string{}

	notional := intent.MaxNotional
	// 输入健壮性校验：负金额、NaN/Inf、无效组合价值一律拒绝，防止绕过风控（杜绝 panic/越界）
	if math.IsNaN(notional) || math.IsInf(notional, 0) || notional <= 0 {
		violations = append(violations, fmt.Sprintf("Invalid order notional %v", notional))
	}
	if math.IsNaN(portfolioValue) || math.IsInf(portfolioValue, 0) || portfolioValue <= 0 {
		violations = append(violations, fmt.Sprintf("Invalid portfolio value %v", portfolioValue))
	}

	// 若组合价值非法，无法继续做占比类校验，直接返回拒绝（避免除以 0 / 无效数值）
	if len(violations) > 0 {
		result := PolicyCheckResult{Passed: false, Decision: "BLOCKED", Reason: "Policy check failed", Violations: violations}
		pe.writePolicyLog("order_check", map[string]interface{}{
			"symbol": intent.Symbol, "side": intent.Side, "notional": notional,
			"decision": result.Decision, "violations": violations,
		})
		return result
	}

	// 单票仓位：新增订单名义价值 + 该标的既有持仓，不得超过 MaxSinglePosition 比例
	notionalFrac := notional / portfolioValue
	if notional > pe.hardLimits.MaxOrderSize {
		violations = append(violations, fmt.Sprintf("Order size %.0f exceeds max %.0f", notional, pe.hardLimits.MaxOrderSize))
	}

	var existing float64
	if currentPositions != nil {
		existing = currentPositions[intent.Symbol]
	}
	if notionalFrac > pe.hardLimits.MaxSinglePosition {
		violations = append(violations, fmt.Sprintf("Single position %.1f%% exceeds max %.1f%%", notionalFrac*100, pe.hardLimits.MaxSinglePosition*100))
	}
	if existing+notionalFrac > pe.hardLimits.MaxSinglePosition {
		violations = append(violations, fmt.Sprintf("Total position(含既有持仓) %.1f%% exceeds max %.1f%%", (existing+notionalFrac)*100, pe.hardLimits.MaxSinglePosition*100))
	}

	// 组合杠杆：既有持仓总市值 + 新订单 不得超过 MaxPortfolioLeverage 倍组合价值
	totalExposure := notional / portfolioValue
	if currentPositions != nil {
		for sym, pos := range currentPositions {
			if sym == intent.Symbol {
				continue // 该标的已计入新增 notionalFrac，避免重复
			}
			totalExposure += pos
		}
	}
	if totalExposure > pe.hardLimits.MaxPortfolioLeverage {
		violations = append(violations, fmt.Sprintf("Portfolio leverage %.2fx exceeds max %.2fx", totalExposure, pe.hardLimits.MaxPortfolioLeverage))
	}

	// 行业暴露：按行业聚合（既有持仓 - 本订单既有同一标的权重 + 新订单权重），任一行业不得超过 MaxSectorExposure。
	// 依赖 SectorProvider 注入的股票->行业数据；未注入提供者时自动跳过该维度。
	if pe.sectorProvider != nil {
		sectorExposure := map[string]float64{}
		for sym, frac := range currentPositions {
			if sym == intent.Symbol {
				continue // 该标的既有权重在下方按"移除既有+计入新增"处理，避免重复
			}
			if sec := pe.sectorOf(sym); sec != "" {
				sectorExposure[sec] += frac
			}
		}
		if sec := pe.sectorOf(intent.Symbol); sec != "" {
			if existing, ok := currentPositions[intent.Symbol]; ok {
				sectorExposure[sec] -= existing // 移除本订单标底的既有权重
			}
			sectorExposure[sec] += notionalFrac // 计入新订单权重
		}
		for sec, exp := range sectorExposure {
			if exp > pe.hardLimits.MaxSectorExposure {
				violations = append(violations, fmt.Sprintf("Sector %s exposure %.1f%% exceeds max %.1f%%", sec, exp*100, pe.hardLimits.MaxSectorExposure*100))
			}
		}
	}

	result := PolicyCheckResult{
		Passed:     len(violations) == 0,
		Decision:   "PASSED",
		Reason:     "",
		Violations: violations,
	}

	if !result.Passed {
		result.Decision = "BLOCKED"
		result.Reason = "Policy check failed"
	}

	pe.writePolicyLog("order_check", map[string]interface{}{
		"symbol":     intent.Symbol,
		"side":       intent.Side,
		"notional":   notional,
		"decision":   result.Decision,
		"violations": violations,
	})

	return result
}

// CheckPortfolioRisk 检查组合风险
func (pe *PolicyEngine) CheckPortfolioRisk(dailyPnL float64, portfolioValue float64, currentDrawdown float64) PolicyCheckResult {
	if pe.emergencyStop {
		return PolicyCheckResult{
			Passed:     false,
			Decision:   "BLOCKED",
			Reason:     "EMERGENCY_STOP",
			Violations: []string{"emergency_stop_active"},
		}
	}

	violations := []string{}

	dailyLossPct := 0.0
	if portfolioValue > 0 {
		dailyLossPct = -dailyPnL / portfolioValue
	}

	if dailyLossPct > pe.hardLimits.MaxDailyLoss {
		violations = append(violations, fmt.Sprintf("Daily loss %.2f%% exceeds max %.2f%%", dailyLossPct*100, pe.hardLimits.MaxDailyLoss*100))
	}

	if currentDrawdown > pe.hardLimits.MaxDrawdown {
		violations = append(violations, fmt.Sprintf("Drawdown %.2f%% exceeds max %.2f%%", currentDrawdown*100, pe.hardLimits.MaxDrawdown*100))
	}

	result := PolicyCheckResult{
		Passed:     len(violations) == 0,
		Decision:   "PASSED",
		Reason:     "",
		Violations: violations,
	}

	if !result.Passed {
		result.Decision = "BLOCKED"
		result.Reason = "Portfolio risk check failed"
	}

	pe.writePolicyLog("portfolio_check", map[string]interface{}{
		"daily_pnl":      dailyPnL,
		"daily_loss_pct": dailyLossPct,
		"drawdown":       currentDrawdown,
		"decision":       result.Decision,
		"violations":     violations,
	})

	return result
}

// ValidateDecision 验证CIO决策
// currentPositions 为组合当前持仓占比 map[代码]float64（与 CheckOrderIntent 口径一致），
// 用于单票/行业/杠杆校验的"既有持仓"，使行业暴露等反映整个组合而非仅单笔订单
func (pe *PolicyEngine) ValidateDecision(decision port.CIODecision, portfolioValue float64, currentPositions map[string]float64) PolicyCheckResult {
	if pe.emergencyStop {
		return PolicyCheckResult{
			Passed:     false,
			Decision:   "BLOCKED",
			Reason:     "EMERGENCY_STOP - All decisions blocked",
			Violations: []string{"emergency_stop_active"},
		}
	}

	violations := []string{}

	for _, order := range decision.Orders {
		result := pe.CheckOrderIntent(order, portfolioValue, currentPositions)
		if !result.Passed {
			violations = append(violations, result.Violations...)
		}
	}

	result := PolicyCheckResult{
		Passed:     len(violations) == 0,
		Decision:   "PASSED",
		Reason:     "",
		Violations: violations,
	}

	if !result.Passed {
		result.Decision = "BLOCKED"
		result.Reason = "Decision policy validation failed"
	}

	pe.writePolicyLog("decision_validation", map[string]interface{}{
		"decision_id":     decision.DecisionID,
		"decision":        decision.Decision,
		"decision_result": result.Decision,
		"violations":      violations,
	})

	return result
}

// GetHardLimits 获取硬限制
func (pe *PolicyEngine) GetHardLimits() HardLimits {
	return pe.hardLimits
}

// writePolicyLog 写入Policy日志
func (pe *PolicyEngine) writePolicyLog(action string, details interface{}) {
	if pe.db == nil {
		return
	}

	logJSON, err := json.Marshal(details)
	if err != nil {
		log.Printf("[Policy] Failed to marshal details: %v, using empty object", err)
		logJSON = []byte("{}")
	}

	policyLog := data.PolicyLog{
		Action:      action,
		DetailsJSON: string(logJSON),
		Timestamp:   time.Now(),
	}

	util.SafeGoWithRetry("Policy.writeLog", 3, func() error {
		return pe.db.GetDB().Create(&policyLog).Error
	})
}
