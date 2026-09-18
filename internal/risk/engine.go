package risk

import (
	"fmt"
	"math"
	"time"

	"github.com/quantpilot/quantpilot/internal/data"
)

// 收益率频率常量：VaR/ES 输入需明确频率，避免混用不同频率数据导致指标不可比
const (
	ReturnFrequencyDaily   = "daily"
	ReturnFrequencyMonthly = "monthly"
	ReturnFrequencyAnnual  = "annual"
)

// validateReturnFrequency 校验收益率频率参数
func validateReturnFrequency(frequency string) error {
	switch frequency {
	case ReturnFrequencyDaily, ReturnFrequencyMonthly, ReturnFrequencyAnnual:
		return nil
	default:
		return fmt.Errorf("无效的收益率频率: %q（支持 daily/monthly/annual）", frequency)
	}
}

type RiskMetrics struct {
	VaR95        float64
	VaR99        float64
	CVaR95       float64
	Volatility   float64
	SharpeRatio  float64
	MaxDrawdown  float64
	Beta         float64
	R2           float64
	SortinoRatio float64
	CalmarRatio  float64
	DownsideDev  float64
	UpsideDev    float64
}

type StopLossConfig struct {
	InitialStop    float64
	TrailingStop   float64
	ATRMultiplier  float64
	MaxHoldingDays int
	ProfitLockPct  float64
	ProfitLockStop float64
}

type SectorExposure struct {
	Sector     string
	Weight     float64
	MaxWeight  float64
	CurrentPnL float64
}

type RiskAlert struct {
	PositionID string
	AlertType  string
	Severity   string
	Message    string
	CurrentPrice float64
	StopPrice  float64
	TriggeredAt time.Time
}

type RiskReport struct {
	GeneratedAt time.Time
	Metrics     RiskMetrics
	Alerts      []RiskAlert
	Violations  []string
	PositionCount int
	TotalExposure float64
}

type RiskEngine struct {
	db             *data.SQLiteManager
	config         StopLossConfig
	positions      map[string]*positionRiskState
	equityCurve    []float64
	returns        []float64
	initialCapital float64
}

type positionRiskState struct {
	positionID   string
	entryPrice   float64
	highestPrice float64
	openDate     time.Time
	peakPrice    float64
}

func DefaultStopLossConfig() StopLossConfig {
	return StopLossConfig{
		InitialStop:    0.05,
		TrailingStop:   0.03,
		ATRMultiplier:  2.0,
		MaxHoldingDays: 120,
		ProfitLockPct:  0.20,
		ProfitLockStop: 0.10,
	}
}

func NewRiskEngine(db *data.SQLiteManager) *RiskEngine {
	return &RiskEngine{
		db:        db,
		config:    DefaultStopLossConfig(),
		positions: make(map[string]*positionRiskState),
	}
}

func NewRiskEngineWithConfig(db *data.SQLiteManager, config StopLossConfig) *RiskEngine {
	return &RiskEngine{
		db:        db,
		config:    config,
		positions: make(map[string]*positionRiskState),
	}
}

func (re *RiskEngine) UpdateConfig(config StopLossConfig) {
	re.config = config
}

func (re *RiskEngine) RecordReturn(ret float64) {
	re.returns = append(re.returns, ret)
}

func (re *RiskEngine) GetReturns() []float64 {
	return re.returns
}

func (re *RiskEngine) RecordEquity(value float64) {
	re.equityCurve = append(re.equityCurve, value)
}

func (re *RiskEngine) SetInitialCapital(capital float64) {
	re.initialCapital = capital
}

func (re *RiskEngine) RegisterPosition(positionID string, entryPrice float64, openDate time.Time) {
	re.positions[positionID] = &positionRiskState{
		positionID:   positionID,
		entryPrice:   entryPrice,
		highestPrice: entryPrice,
		openDate:     openDate,
		peakPrice:    entryPrice,
	}
}

func (re *RiskEngine) UpdatePositionPeak(positionID string, price float64) {
	pos, ok := re.positions[positionID]
	if !ok {
		return
	}
	if price > pos.peakPrice {
		pos.peakPrice = price
	}
	if price > pos.highestPrice {
		pos.highestPrice = price
	}
}

func (re *RiskEngine) RemovePosition(positionID string) {
	delete(re.positions, positionID)
}

// CalculateVaR 历史法 VaR
// frequency 指定收益率频率（daily/monthly/annual），明确输入契约避免混用
func (re *RiskEngine) CalculateVaR(returns []float64, confidence float64, frequency string) float64 {
	if len(returns) == 0 {
		return 0
	}
	if err := validateReturnFrequency(frequency); err != nil {
		return 0
	}
	sorted := make([]float64, len(returns))
	copy(sorted, returns)
	insertionSort(sorted)

	idx := int(math.Floor((1 - confidence) * float64(len(sorted))))
	if idx < 0 {
		idx = 0
	}
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}

	return math.Abs(sorted[idx])
}

// CalculateCVaR 历史法 CVaR（ES）
// frequency 指定收益率频率（daily/monthly/annual），明确输入契约避免混用
func (re *RiskEngine) CalculateCVaR(returns []float64, confidence float64, frequency string) float64 {
	if len(returns) == 0 {
		return 0
	}
	if err := validateReturnFrequency(frequency); err != nil {
		return 0
	}
	sorted := make([]float64, len(returns))
	copy(sorted, returns)
	insertionSort(sorted)

	idx := int(math.Floor((1 - confidence) * float64(len(sorted))))
	if idx < 0 {
		idx = 0
	}
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}

	tail := sorted[:idx+1]
	if len(tail) == 0 {
		return 0
	}

	sum := 0.0
	for _, r := range tail {
		sum += r
	}
	avg := sum / float64(len(tail))
	return math.Abs(avg)
}

func (re *RiskEngine) CalculateVolatility(returns []float64, annualizeDays int) float64 {
	if len(returns) < 2 {
		return 0
	}

	mean := 0.0
	for _, r := range returns {
		mean += r
	}
	mean /= float64(len(returns))

	variance := 0.0
	for _, r := range returns {
		diff := r - mean
		variance += diff * diff
	}
	variance /= float64(len(returns) - 1)

	stdDev := math.Sqrt(variance)
	if annualizeDays <= 0 {
		annualizeDays = 252
	}

	return stdDev * math.Sqrt(float64(annualizeDays))
}

func (re *RiskEngine) CalculateSharpeRatio(returns []float64, riskFreeRate float64) float64 {
	if len(returns) < 2 {
		return 0
	}

	mean := 0.0
	for _, r := range returns {
		mean += r
	}
	mean /= float64(len(returns))

	variance := 0.0
	for _, r := range returns {
		diff := r - mean
		variance += diff * diff
	}
	variance /= float64(len(returns) - 1)

	stdDev := math.Sqrt(variance)
	if stdDev == 0 {
		return 0
	}

	excessReturn := mean - riskFreeRate
	return excessReturn / stdDev
}

// CalculateSortinoRatioPublic 公开的SortinoRatio计算方法
func (re *RiskEngine) CalculateSortinoRatioPublic(returns []float64, riskFreeRate float64) float64 {
	return re.calculateSortinoRatio(returns, riskFreeRate)
}

// CalculateCalmarRatioPublic 公开的CalmarRatio计算方法
func (re *RiskEngine) CalculateCalmarRatioPublic(returns []float64, equityCurve []float64) float64 {
	return re.calculateCalmarRatio(returns, equityCurve)
}

// CalculateDownsideDeviationPublic 公开的下行偏差计算方法
func (re *RiskEngine) CalculateDownsideDeviationPublic(returns []float64) float64 {
	return re.calculateDownsideDeviation(returns)
}

func (re *RiskEngine) CalculateMaxDrawdown(equityCurve []float64) float64 {
	if len(equityCurve) < 2 {
		return 0
	}

	peak := equityCurve[0]
	maxDD := 0.0

	for _, v := range equityCurve {
		if v > peak {
			peak = v
		}
		dd := (peak - v) / peak
		if dd > maxDD {
			maxDD = dd
		}
	}

	return maxDD
}

func (re *RiskEngine) CalculateBeta(stockReturns []float64, marketReturns []float64) float64 {
	n := len(stockReturns)
	if n < 2 || len(marketReturns) < n {
		return 0
	}

	stockMean := 0.0
	marketMean := 0.0
	for i := 0; i < n; i++ {
		stockMean += stockReturns[i]
		marketMean += marketReturns[i]
	}
	stockMean /= float64(n)
	marketMean /= float64(n)

	covariance := 0.0
	marketVariance := 0.0
	for i := 0; i < n; i++ {
		sDiff := stockReturns[i] - stockMean
		mDiff := marketReturns[i] - marketMean
		covariance += sDiff * mDiff
		marketVariance += mDiff * mDiff
	}
	covariance /= float64(n - 1)
	marketVariance /= float64(n - 1)

	if marketVariance == 0 {
		return 0
	}

	return covariance / marketVariance
}

func (re *RiskEngine) CalculateATR(high, low, close []float64, period int) float64 {
	n := len(close)
	if n < period+1 || len(high) < n || len(low) < n {
		return 0
	}

	trueRanges := make([]float64, n-1)
	for i := 1; i < n; i++ {
		tr := high[i] - low[i]
		hc := math.Abs(high[i] - close[i-1])
		lc := math.Abs(low[i] - close[i-1])
		if hc > tr {
			tr = hc
		}
		if lc > tr {
			tr = lc
		}
		trueRanges[i-1] = tr
	}

	if len(trueRanges) < period {
		period = len(trueRanges)
	}

	sum := 0.0
	for i := len(trueRanges) - period; i < len(trueRanges); i++ {
		sum += trueRanges[i]
	}

	return sum / float64(period)
}

func (re *RiskEngine) CalculateTrailingStop(entryPrice, highestPrice float64, config StopLossConfig) float64 {
	if config.TrailingStop <= 0 {
		return entryPrice * (1 - config.InitialStop)
	}

	trailingStopPrice := highestPrice * (1 - config.TrailingStop)

	if config.InitialStop > 0 {
		initialStopPrice := entryPrice * (1 - config.InitialStop)
		if trailingStopPrice < initialStopPrice {
			return initialStopPrice
		}
	}

	return trailingStopPrice
}

func (re *RiskEngine) CheckStopLossTriggered(currentPrice, entryPrice float64, config StopLossConfig) (bool, string) {
	if config.InitialStop > 0 {
		stopPrice := entryPrice * (1 - config.InitialStop)
		if currentPrice <= stopPrice {
			return true, fmt.Sprintf("触发初始止损: 当前价 %.4f <= 止损价 %.4f (%.0f%%)", currentPrice, stopPrice, config.InitialStop*100)
		}
	}

	if config.TrailingStop > 0 && currentPrice > entryPrice {
		return false, ""
	}

	return false, ""
}

func (re *RiskEngine) CheckTakeProfitTriggered(currentPrice, entryPrice float64, peakPrice float64, config StopLossConfig) (bool, string) {
	if config.ProfitLockPct > 0 {
		profitFromEntry := (peakPrice - entryPrice) / entryPrice
		if profitFromEntry >= config.ProfitLockPct && config.ProfitLockStop > 0 {
			dropFromPeak := (peakPrice - currentPrice) / peakPrice
			if dropFromPeak >= config.ProfitLockStop {
				return true, fmt.Sprintf("触发止盈锁定: 从峰值 %.4f 回撤 %.2f%%, 当前价 %.4f", peakPrice, dropFromPeak*100, currentPrice)
			}
		}
	}

	return false, ""
}

func (re *RiskEngine) CheckSectorExposure(sectors []SectorExposure, maxSectorWeight float64) []string {
	var violations []string
	for _, s := range sectors {
		if s.Weight > maxSectorWeight {
			violations = append(violations, fmt.Sprintf("行业 [%s] 权重 %.2f%% 超过限制 %.2f%%", s.Sector, s.Weight*100, maxSectorWeight*100))
		}
		if s.MaxWeight > 0 && s.Weight > s.MaxWeight {
			violations = append(violations, fmt.Sprintf("行业 [%s] 权重 %.2f%% 超过行业上限 %.2f%%", s.Sector, s.Weight*100, s.MaxWeight*100))
		}
	}
	return violations
}

func (re *RiskEngine) CheckConcentrationRisk(weights map[string]float64, maxSingleWeight float64) []string {
	var violations []string
	for name, w := range weights {
		if w > maxSingleWeight {
			violations = append(violations, fmt.Sprintf("持仓 [%s] 权重 %.2f%% 超过单一上限 %.2f%%", name, w*100, maxSingleWeight*100))
		}
	}
	return violations
}

func (re *RiskEngine) CalculateDiversificationRatio(weights map[string]float64, returns map[string][]float64) float64 {
	if len(weights) == 0 || len(returns) == 0 {
		return 0
	}

	var weightedVolSum float64
	var portfolioVariance float64
	assets := make([]string, 0, len(weights))
	for name := range weights {
		assets = append(assets, name)
	}

	for _, name := range assets {
		w := weights[name]
		ret, ok := returns[name]
		if !ok || len(ret) < 2 {
			continue
		}

		mean := 0.0
		for _, r := range ret {
			mean += r
		}
		mean /= float64(len(ret))

		variance := 0.0
		for _, r := range ret {
			diff := r - mean
			variance += diff * diff
		}
		variance /= float64(len(ret) - 1)
		vol := math.Sqrt(variance)

		weightedVolSum += w * vol
		portfolioVariance += w * w * variance
	}

	portfolioVol := math.Sqrt(portfolioVariance)
	if portfolioVol == 0 {
		return 0
	}

	return weightedVolSum / portfolioVol
}

func (re *RiskEngine) RunStressTest(portfolioValue float64, scenarios map[string]float64) map[string]float64 {
	results := make(map[string]float64, len(scenarios))
	for name, shock := range scenarios {
		impact := portfolioValue * shock
		results[name] = impact
	}
	return results
}

func (re *RiskEngine) MonitorPositionRisk(positionID string, currentPrice, entryPrice float64, config StopLossConfig) RiskAlert {
	re.RegisterPosition(positionID, entryPrice, time.Now())
	re.UpdatePositionPeak(positionID, currentPrice)

	pos, ok := re.positions[positionID]
	if !ok {
		return RiskAlert{
			PositionID:  positionID,
			AlertType:   "error",
			Severity:    "high",
			Message:     "持仓未找到",
			TriggeredAt: time.Now(),
		}
	}

	var alert RiskAlert
	alert.PositionID = positionID
	alert.CurrentPrice = currentPrice
	alert.TriggeredAt = time.Now()

	stopTriggered, reason := re.CheckStopLossTriggered(currentPrice, entryPrice, config)
	if stopTriggered {
		alert.AlertType = "stop_loss"
		alert.Severity = "critical"
		alert.Message = reason
		alert.StopPrice = entryPrice * (1 - config.InitialStop)
		return alert
	}

	if config.TrailingStop > 0 {
		trailingStop := re.CalculateTrailingStop(entryPrice, pos.peakPrice, config)
		if currentPrice <= trailingStop && pos.peakPrice > entryPrice {
			alert.AlertType = "trailing_stop"
			alert.Severity = "high"
			alert.Message = fmt.Sprintf("触发移动止损: 当前价 %.4f <= 移动止损价 %.4f", currentPrice, trailingStop)
			alert.StopPrice = trailingStop
			return alert
		}
	}

	profitTriggered, profitReason := re.CheckTakeProfitTriggered(currentPrice, entryPrice, pos.peakPrice, config)
	if profitTriggered {
		alert.AlertType = "take_profit"
		alert.Severity = "high"
		alert.Message = profitReason
		alert.StopPrice = pos.peakPrice * (1 - config.ProfitLockStop)
		return alert
	}

	holdingDays := int(time.Since(pos.openDate).Hours() / 24)
	if config.MaxHoldingDays > 0 && holdingDays >= config.MaxHoldingDays {
		alert.AlertType = "max_holding"
		alert.Severity = "medium"
		alert.Message = fmt.Sprintf("持仓 %d 天已达上限 %d 天", holdingDays, config.MaxHoldingDays)
		alert.StopPrice = currentPrice
		return alert
	}

	_ = reason
	alert.AlertType = "ok"
	alert.Severity = "info"
	alert.Message = fmt.Sprintf("持仓正常: 盈亏 %.2f%%", (currentPrice-entryPrice)/entryPrice*100)
	alert.StopPrice = entryPrice * (1 - config.InitialStop)
	return alert
}

func (re *RiskEngine) GenerateRiskReport(metrics RiskMetrics, positions []SectorExposure) RiskReport {
	alerts := make([]RiskAlert, 0)
	totalExposure := 0.0
	for _, p := range positions {
		totalExposure += p.Weight
	}

	return RiskReport{
		GeneratedAt:   time.Now(),
		Metrics:        metrics,
		Alerts:         alerts,
		PositionCount:  len(positions),
		TotalExposure:  totalExposure,
	}
}

func (re *RiskEngine) ComputeFullMetrics() RiskMetrics {
	return RiskMetrics{
		VaR95:        re.CalculateVaR(re.returns, 0.95, ReturnFrequencyDaily),
		VaR99:        re.CalculateVaR(re.returns, 0.99, ReturnFrequencyDaily),
		CVaR95:       re.CalculateCVaR(re.returns, 0.95, ReturnFrequencyDaily),
		Volatility:   re.CalculateVolatility(re.returns, 252),
		SharpeRatio:  re.CalculateSharpeRatio(re.returns, 0.03),
		MaxDrawdown:  re.CalculateMaxDrawdown(re.equityCurve),
		Beta:         0,
		R2:           0,
		SortinoRatio: re.calculateSortinoRatio(re.returns, 0.03),
		CalmarRatio:  re.calculateCalmarRatio(re.returns, re.equityCurve),
		DownsideDev:  re.calculateDownsideDeviation(re.returns),
		UpsideDev:    re.calculateUpsideDeviation(re.returns),
	}
}

func (re *RiskEngine) calculateSortinoRatio(returns []float64, riskFreeRate float64) float64 {
	if len(returns) < 2 {
		return 0
	}

	downsideReturns := make([]float64, 0)
	for _, r := range returns {
		if r < 0 {
			downsideReturns = append(downsideReturns, r)
		}
	}

	if len(downsideReturns) < 2 {
		return 0
	}

	downsideMean := 0.0
	for _, r := range downsideReturns {
		downsideMean += r
	}
	downsideMean /= float64(len(downsideReturns))

	downsideVar := 0.0
	for _, r := range downsideReturns {
		diff := r - downsideMean
		downsideVar += diff * diff
	}
	downsideVar /= float64(len(downsideReturns) - 1)

	downsideDev := math.Sqrt(downsideVar)
	if downsideDev == 0 {
		return 0
	}

	meanReturn := 0.0
	for _, r := range returns {
		meanReturn += r
	}
	meanReturn /= float64(len(returns))

	excessReturn := meanReturn - riskFreeRate
	return excessReturn / downsideDev
}

func (re *RiskEngine) calculateCalmarRatio(returns []float64, equityCurve []float64) float64 {
	if len(returns) < 2 {
		return 0
	}

	meanReturn := 0.0
	for _, r := range returns {
		meanReturn += r
	}
	meanReturn /= float64(len(returns))

	annualizedReturn := meanReturn * 252
	maxDD := re.CalculateMaxDrawdown(equityCurve)
	if maxDD == 0 {
		return 0
	}

	return annualizedReturn / maxDD
}

func (re *RiskEngine) calculateDownsideDeviation(returns []float64) float64 {
	if len(returns) < 2 {
		return 0
	}

	downside := make([]float64, 0)
	for _, r := range returns {
		if r < 0 {
			downside = append(downside, r*r)
		} else {
			downside = append(downside, 0)
		}
	}

	sum := 0.0
	for _, d := range downside {
		sum += d
	}

	return math.Sqrt(sum / float64(len(downside)))
}

func (re *RiskEngine) calculateUpsideDeviation(returns []float64) float64 {
	if len(returns) < 2 {
		return 0
	}

	upside := make([]float64, 0)
	for _, r := range returns {
		if r > 0 {
			upside = append(upside, r*r)
		} else {
			upside = append(upside, 0)
		}
	}

	sum := 0.0
	for _, u := range upside {
		sum += u
	}

	return math.Sqrt(sum / float64(len(upside)))
}

func insertionSort(arr []float64) {
	for i := 1; i < len(arr); i++ {
		key := arr[i]
		j := i - 1
		for j >= 0 && arr[j] > key {
			arr[j+1] = arr[j]
			j--
		}
		arr[j+1] = key
	}
}