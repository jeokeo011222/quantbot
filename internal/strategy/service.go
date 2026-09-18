package strategy

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/quantpilot/quantpilot/internal/backtest"
	"github.com/quantpilot/quantpilot/internal/data"
	"github.com/quantpilot/quantpilot/internal/tdx"
)

// Service 策略管理服务
type Service struct {
	db        *data.SQLiteManager
	refreshMu sync.Mutex // 串行化指标刷新，避免启动后台刷新与调优后回调并发写库
}

// NewService 创建策略管理服务
func NewService(db *data.SQLiteManager) *Service {
	return &Service{db: db}
}

// StrategyDTO 策略数据传输对象
type StrategyDTO struct {
	ID             uint    `json:"id"`
	Name           string  `json:"name"`
	Description    string  `json:"description"`
	StrategyType   string  `json:"strategy_type"`
	Status         string  `json:"status"`     // running/stopped
	IsBuiltin      bool    `json:"is_builtin"` // 是否为内置策略
	SharpeRatio    float64 `json:"sharpe_ratio"`
	MaxDrawdown    float64 `json:"max_drawdown"`
	TotalReturn    float64 `json:"total_return"`
	WinRate        float64 `json:"win_rate"`
	Turnover       float64 `json:"turnover"`
	InitialCapital float64 `json:"initial_capital"`
	MaxPosition    int     `json:"max_position"`
	StopLossPct    float64 `json:"stop_loss_pct"`
	TakeProfitPct  float64 `json:"take_profit_pct"`
	CreatedAt      string  `json:"created_at"`
	UpdatedAt      string  `json:"updated_at"`
}

// CreateStrategyRequest 创建策略请求
type CreateStrategyRequest struct {
	Name           string  `json:"name"`
	Description    string  `json:"description"`
	StrategyType   string  `json:"strategy_type"`
	InitialCapital float64 `json:"initial_capital"`
	MaxPosition    int     `json:"max_position"`
	StopLossPct    float64 `json:"stop_loss_pct"`
	TakeProfitPct  float64 `json:"take_profit_pct"`
	ConfigJSON     string  `json:"config_json"`
	OperatorID     string  `json:"operator_id"`
	OperatorName   string  `json:"operator_name"`
}

// UpdateStrategyRequest 更新策略请求
type UpdateStrategyRequest struct {
	ID             uint    `json:"id"`
	Name           string  `json:"name"`
	Description    string  `json:"description"`
	StrategyType   string  `json:"strategy_type"`
	InitialCapital float64 `json:"initial_capital"`
	MaxPosition    int     `json:"max_position"`
	StopLossPct    float64 `json:"stop_loss_pct"`
	TakeProfitPct  float64 `json:"take_profit_pct"`
}

// GetStrategies 获取策略列表
func (s *Service) GetStrategies(status string) ([]*StrategyDTO, error) {
	db := s.db.GetDB()
	if db == nil {
		return nil, fmt.Errorf("数据库未初始化")
	}

	var strategies []data.Strategy
	query := db.Model(&data.Strategy{})

	if status == "running" {
		query = query.Where("is_active = ?", 1)
	} else if status == "stopped" {
		query = query.Where("is_active = ?", 0)
	}

	if err := query.Order("created_at DESC").Find(&strategies).Error; err != nil {
		return nil, fmt.Errorf("获取策略列表失败: %w", err)
	}

	result := make([]*StrategyDTO, len(strategies))
	for i, st := range strategies {
		result[i] = s.toDTO(&st)
	}

	return result, nil
}

// GetStrategy 获取单个策略
func (s *Service) GetStrategy(id uint) (*StrategyDTO, error) {
	db := s.db.GetDB()
	if db == nil {
		return nil, fmt.Errorf("数据库未初始化")
	}

	var strategy data.Strategy
	if err := db.First(&strategy, id).Error; err != nil {
		return nil, fmt.Errorf("策略不存在: %d", id)
	}

	return s.toDTO(&strategy), nil
}

// CreateStrategy 创建策略
func (s *Service) CreateStrategy(req *CreateStrategyRequest) (*StrategyDTO, error) {
	db := s.db.GetDB()
	if db == nil {
		return nil, fmt.Errorf("数据库未初始化")
	}

	strategy := &data.Strategy{
		Name:           req.Name,
		Description:    req.Description,
		StrategyType:   req.StrategyType,
		IsActive:       0, // 默认停止状态
		ConfigJSON:     "{}",
		UniverseJSON:   "[]",
		InitialCapital: req.InitialCapital,
		MaxPosition:    req.MaxPosition,
		StopLossPct:    req.StopLossPct,
		TakeProfitPct:  req.TakeProfitPct,
	}

	if strategy.InitialCapital == 0 {
		strategy.InitialCapital = 100000
	}
	if strategy.MaxPosition == 0 {
		strategy.MaxPosition = 10
	}
	if strategy.StopLossPct == 0 {
		strategy.StopLossPct = 0.05
	}
	if strategy.TakeProfitPct == 0 {
		strategy.TakeProfitPct = 0.20
	}

	// 使用传入的ConfigJSON参数（如果有的话），否则构建默认配置
	if req.ConfigJSON != "" && req.ConfigJSON != "{}" {
		strategy.ConfigJSON = req.ConfigJSON
	} else {
		// 构建初始配置JSON
		config := map[string]interface{}{
			"capital":       strategy.InitialCapital,
			"max_position":  strategy.MaxPosition,
			"stop_loss":     strategy.StopLossPct,
			"take_profit":   strategy.TakeProfitPct,
			"strategy_type": strategy.StrategyType,
		}
		configJSON, _ := json.Marshal(config)
		strategy.ConfigJSON = string(configJSON)
	}

	if err := db.Create(strategy).Error; err != nil {
		return nil, fmt.Errorf("创建策略失败: %w", err)
	}

	log.Printf("[StrategyService] Created strategy: %s (ID: %d)", strategy.Name, strategy.ID)

	return s.toDTO(strategy), nil
}

// UpdateStrategy 更新策略
func (s *Service) UpdateStrategy(req *UpdateStrategyRequest) (*StrategyDTO, error) {
	db := s.db.GetDB()
	if db == nil {
		return nil, fmt.Errorf("数据库未初始化")
	}

	var strategy data.Strategy
	if err := db.First(&strategy, req.ID).Error; err != nil {
		return nil, fmt.Errorf("策略不存在: %d", req.ID)
	}

	if req.Name != "" {
		strategy.Name = req.Name
	}
	if req.Description != "" {
		strategy.Description = req.Description
	}
	if req.StrategyType != "" {
		strategy.StrategyType = req.StrategyType
	}
	if req.InitialCapital > 0 {
		strategy.InitialCapital = req.InitialCapital
	}
	if req.MaxPosition > 0 {
		strategy.MaxPosition = req.MaxPosition
	}
	if req.StopLossPct > 0 {
		strategy.StopLossPct = req.StopLossPct
	}
	if req.TakeProfitPct > 0 {
		strategy.TakeProfitPct = req.TakeProfitPct
	}

	strategy.UpdatedAt = time.Now()

	if err := db.Save(&strategy).Error; err != nil {
		return nil, fmt.Errorf("更新策略失败: %w", err)
	}

	log.Printf("[StrategyService] Updated strategy: %s (ID: %d)", strategy.Name, strategy.ID)

	return s.toDTO(&strategy), nil
}

// DeleteStrategy 删除策略
func (s *Service) DeleteStrategy(id uint) error {
	db := s.db.GetDB()
	if db == nil {
		return fmt.Errorf("数据库未初始化")
	}

	result := db.Delete(&data.Strategy{}, id)
	if result.Error != nil {
		return fmt.Errorf("删除策略失败: %w", result.Error)
	}

	log.Printf("[StrategyService] Deleted strategy: ID %d, rows affected: %d", id, result.RowsAffected)
	return nil
}

// ToggleStrategyStatus 切换策略状态
func (s *Service) ToggleStrategyStatus(id uint) (*StrategyDTO, error) {
	db := s.db.GetDB()
	if db == nil {
		return nil, fmt.Errorf("数据库未初始化")
	}

	var strategy data.Strategy
	if err := db.First(&strategy, id).Error; err != nil {
		return nil, fmt.Errorf("策略不存在: %d", id)
	}

	if strategy.IsActive == 1 {
		strategy.IsActive = 0
	} else {
		strategy.IsActive = 1
	}
	strategy.UpdatedAt = time.Now()

	if err := db.Save(&strategy).Error; err != nil {
		return nil, fmt.Errorf("切换策略状态失败: %w", err)
	}

	log.Printf("[StrategyService] Toggled strategy status: %s (ID: %d), now: %d", strategy.Name, strategy.ID, strategy.IsActive)

	return s.toDTO(&strategy), nil
}

// UpdateStrategyMetrics 更新策略表现指标（回测后调用）
func (s *Service) UpdateStrategyMetrics(id uint, sharpe, maxDD, totalReturn, winRate, turnover float64) error {
	db := s.db.GetDB()
	if db == nil {
		return fmt.Errorf("数据库未初始化")
	}

	result := db.Model(&data.Strategy{}).
		Where("id = ?", id).
		Updates(map[string]interface{}{
			"sharpe_ratio": sharpe,
			"max_drawdown": maxDD,
			"total_return": totalReturn,
			"win_rate":     winRate,
			"turnover":     turnover,
			"updated_at":   time.Now(),
		})

	if result.Error != nil {
		return fmt.Errorf("更新策略指标失败: %w", result.Error)
	}

	return nil
}

// EnsureDefaultStrategies 确保默认策略存在
// 策略定义统一来自 backtest.BuiltinStrategies（单一数据源），不再硬编码任何指标。
// 同时清理历史遗留的重复策略（动量/均值回归/质量因子/多因子组合）。
func (s *Service) EnsureDefaultStrategies() error {
	db := s.db.GetDB()
	if db == nil {
		return fmt.Errorf("数据库未初始化")
	}

	// 1. 清理历史遗留重复/无实现策略（统一策略表）。
	// fundamental（基本面因子策略）因历史财务数据年份不足无法使用，一并清理。
	legacyTypes := []string{"momentum", "mean_reversion", "quality", "multi_factor", "fundamental"}
	cleanup := db.Model(&data.Strategy{}).
		Where("is_builtin = ? AND strategy_type IN ?", 1, legacyTypes).
		Delete(&data.Strategy{})
	if cleanup.Error != nil {
		log.Printf("[StrategyService] 清理历史遗留策略失败: %v", cleanup.Error)
	} else if cleanup.RowsAffected > 0 {
		log.Printf("[StrategyService] 已清理 %d 条重复/无实现的历史策略（momentum/mean_reversion/quality/multi_factor/fundamental）", cleanup.RowsAffected)
	}

	// 2. 从统一目录播种内置策略（指标初始为0，由 RefreshStrategyMetrics 用真实回测写入）
	var existingNames []string
	db.Model(&data.Strategy{}).Pluck("name", &existingNames)
	existingSet := make(map[string]bool)
	for _, name := range existingNames {
		existingSet[name] = true
	}

	created := 0
	for _, meta := range backtest.BuiltinStrategies {
		if existingSet[meta.Name] {
			continue // 已存在，跳过
		}
		// 写入该策略可调参数的扫描范围（自动化调参工具使用），参数本身不写死
		tuneRangesJSON := "{}"
		if ranges := backtest.GetDefaultTuneRanges(meta.StrategyType); len(ranges) > 0 {
			if b, err := json.Marshal(ranges); err == nil {
				tuneRangesJSON = string(b)
			}
		}
		st := &data.Strategy{
			Name:           meta.Name,
			Description:    meta.Description,
			StrategyType:   meta.StrategyType,
			IsActive:       0,
			IsBuiltin:      1,
			ConfigJSON:     meta.ConfigJSON,
			UniverseJSON:   "[]",
			TuneRangesJSON: tuneRangesJSON,
			SharpeRatio:    0,
			MaxDrawdown:    0,
			TotalReturn:    0,
			WinRate:        0,
			InitialCapital: 100000,
			MaxPosition:    10,
			StopLossPct:    meta.StopLossPct,
			TakeProfitPct:  meta.TakeProfitPct,
		}
		if err := db.Create(st).Error; err != nil {
			log.Printf("[StrategyService] Failed to create builtin strategy %s: %v", meta.Name, err)
		} else {
			created++
		}
	}

	log.Printf("[StrategyService] Created %d builtin strategies", created)

	// 3. 为已存在的内置策略补齐缺失的调参范围（tune_ranges_json）
	filled := 0
	var builtins []data.Strategy
	db.Model(&data.Strategy{}).Where("is_builtin = ?", 1).Find(&builtins)
	for i := range builtins {
		st := &builtins[i]
		if st.TuneRangesJSON != "" && st.TuneRangesJSON != "{}" {
			continue
		}
		if ranges := backtest.GetDefaultTuneRanges(st.StrategyType); len(ranges) > 0 {
			if b, err := json.Marshal(ranges); err == nil {
				db.Model(&data.Strategy{}).Where("id = ?", st.ID).Update("tune_ranges_json", string(b))
				filled++
			}
		}
	}
	if filled > 0 {
		log.Printf("[StrategyService] 已为 %d 个内置策略补齐调参范围", filled)
	}

	// 统计总策略数量
	var totalCount int64
	db.Model(&data.Strategy{}).Count(&totalCount)
	log.Printf("[StrategyService] Total strategies in database: %d", totalCount)

	return nil
}

// sampleLookback 单样本回测取K线根数（撮合样本量：更长的历史区间在单一标的上产生更多成交，
// 结合多标的样本聚合，保证胜率等指标有足够样本数，避免克隆策略因成交过少导致胜率失真如100%）。
const sampleLookback = 1500

// sampleBars 回测样本：单个标的的K线数据
type sampleBars struct {
	Code string         // 标的代码（持仓股票或基准指数）
	Name string         // 标的名称（用于识别 ST，无法获取时为空）
	Bars []tdx.KlineBar // 升序K线
}

// RefreshStrategyMetrics 用真实回测数据刷新所有内置策略指标（从底层规范数据）。
// 为提升指标样本量、避免单标的回测成交过少导致胜率失真（如仅几笔全赢显示100%），
// 统一采用“样本池”回测：优先以当前持仓股票作为样本（真实成交、数量丰富），
// 持仓为空时回退基准指数（沪深300→上证指数）；对每个策略在每个样本上分别回测，
// 聚合所有样本的已平仓交易，汇总计算胜率等指标。
func (s *Service) RefreshStrategyMetrics(duckdbMgr *data.DuckDBManager) error {
	if duckdbMgr == nil || !duckdbMgr.HasStockDB() {
		return fmt.Errorf("DuckDB 不可用，跳过策略指标刷新")
	}

	// 串行化：启动后台刷新与调优后回调共用同一入口，避免并发写同一批策略行
	s.refreshMu.Lock()
	defer s.refreshMu.Unlock()

	db := s.db.GetDB()
	if db == nil {
		return fmt.Errorf("数据库未初始化")
	}

	// 构建回测样本池（持仓股票 + 基准指数兜底）
	samples, err := s.getSampleBars(duckdbMgr)
	if err != nil {
		return fmt.Errorf("构建回测样本池失败: %w", err)
	}
	// 财务数据提供者（供基本面因子策略按披露日对齐读取真实财务数据，无未来函数）
	finProvider := backtest.NewDuckDBFinancialProvider(duckdbMgr)
	log.Printf("[StrategyService] 指标刷新样本池: %d 个标的", len(samples))
	for _, sm := range samples {
		if len(sm.Bars) > 0 {
			log.Printf("[StrategyService]   样本 %s: %d 根K线 (%s ~ %s)",
				sm.Code, len(sm.Bars), sm.Bars[0].Date, sm.Bars[len(sm.Bars)-1].Date)
		}
	}

	var strategies []data.Strategy
	if err := db.Model(&data.Strategy{}).Where("is_builtin = ?", 1).Find(&strategies).Error; err != nil {
		return fmt.Errorf("查询内置策略失败: %w", err)
	}

	// 构建可执行策略任务列表。样本池 K线只加载一次，各策略在其上背对背回测，
	// 数据加载与并发回测分离，避免每个策略重复拉取行情，从而显著提升整体刷新耗时。
	type strategyTask struct {
		st         *data.Strategy
		strategy   backtest.Strategy
		stopLoss   float64
		takeProfit float64
	}
	var tasks []strategyTask
	skipped := 0
	for i := range strategies {
		st := &strategies[i]
		if !backtest.IsBuiltinStrategyType(st.StrategyType) {
			log.Printf("[StrategyService] 跳过无实现策略 %s (type=%s)", st.Name, st.StrategyType)
			skipped++
			continue
		}

		// 使用策略表配置的止损/止盈，保证指标与策略参数一致（从底层规范数据）
		stopLoss := st.StopLossPct
		if stopLoss <= 0 {
			stopLoss = 0.05
		}
		takeProfit := st.TakeProfitPct
		if takeProfit <= 0 {
			takeProfit = 0.20
		}

		// 从策略表 config_json 读取当前生效参数构造策略实例（参数不写死，由调参工具写入）
		params := backtest.ParseConfigJSON(st.ConfigJSON)
		strategy := backtest.BuildStrategyFromConfig(st.StrategyType, params)
		if strategy == nil {
			log.Printf("[StrategyService] 跳过无法构造的策略 %s (type=%s)", st.Name, st.StrategyType)
			skipped++
			continue
		}
		tasks = append(tasks, strategyTask{st: st, strategy: strategy, stopLoss: stopLoss, takeProfit: takeProfit})
	}

	// 并行回测所有策略：单一数据加载，各策略副本为独立实例互不影响；财务提供者在多线程下
	// 经 database/sql 访问 DuckDB（pool 化，并发安全）。采用有界 worker 池控制并发，避免峰值
	// 打到 DuckDB 连接上限与 CPU 峰值。
	workerCount := len(tasks)
	if workerCount > 8 {
		workerCount = 8
	}
	jobCh := make(chan strategyTask)
	var logMu sync.Mutex
	var refreshed int32
	var wg sync.WaitGroup
	for w := 0; w < workerCount; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for t := range jobCh {
				// 在样本池每个标的上分别回测，聚合已平仓交易与绩效
				aggWins := 0
				aggLosses := 0
				tradeCount := 0
				var returns []float64
				var sharpeVals []float64
				var ddVals []float64
				var turnoverVals []float64
				activeSamples := 0
				for _, sm := range samples {
					if len(sm.Bars) < 20 {
						continue
					}
					// 每个样本资金需足够买入其最高价对应的1手，避免高价格标的“有信号而0成交”
					cap := backtest.EnsureOneLotCapital(sm.Bars, 100000)
					res := backtest.RunBacktestWithLimitFin(sm.Bars, t.strategy, cap, t.stopLoss, t.takeProfit, sm.Code, sm.Name, finProvider)
					aggWins += res.WinTrades
					aggLosses += res.LossTrades
					tradeCount += res.TotalTrades
					// 仅在当前样本实际发生交易时纳入收益/夏普/回撤聚合，避免大量0交易样本拉低均值
					if res.TotalTrades > 0 {
						activeSamples++
						returns = append(returns, (res.FinalCapital-cap)/cap*100)
						sharpeVals = append(sharpeVals, res.SharpeRatio)
						ddVals = append(ddVals, res.MaxDrawdown)
						turnoverVals = append(turnoverVals, res.Turnover)
					}
				}

				// 汇总胜率：以所有样本的已平仓交易为准（WinTrades/LossTrades 仅统计有盈亏的平仓）
				totalTrades := aggWins + aggLosses
				winRate := 0.0
				if totalTrades > 0 {
					winRate = float64(aggWins) / float64(totalTrades) * 100
				}
				// 收益/夏普/回撤/换手取活跃样本的平均值
				sharpe := mean(sharpeVals)
				maxDD := mean(ddVals)
				totalReturn := mean(returns)
				turnover := mean(turnoverVals)

				if err := s.UpdateStrategyMetrics(t.st.ID, sharpe, maxDD, totalReturn, winRate, turnover); err != nil {
					log.Printf("[StrategyService] 刷新策略 %s 指标失败: %v", t.st.Name, err)
					continue
				}
				logMu.Lock()
				log.Printf("[StrategyService] 刷新策略指标 %s: 总收益=%.2f%%, 夏普=%.2f, 回撤=%.2f%%, 胜率=%.1f%%, 换手=%.2fx, 交易=%d (样本%d)",
					t.st.Name, totalReturn, sharpe, maxDD, winRate, turnover, tradeCount, activeSamples)
				logMu.Unlock()
				atomic.AddInt32(&refreshed, 1)
			}
		}()
	}
	for _, t := range tasks {
		jobCh <- t
	}
	close(jobCh)
	wg.Wait()

	log.Printf("[StrategyService] 策略指标刷新完成: 刷新=%d, 跳过=%d", atomic.LoadInt32(&refreshed), skipped)
	return nil
}

// instrumentName 已知标的代码，尽力从 Instrument 表查询其名称（用于 ST 涨跌停判断）。
// 失败或无法获取时返回空字符串，不影响按代码的市场推断。
func (s *Service) instrumentName(code string) string {
	if s.db == nil || s.db.GetDB() == nil {
		return ""
	}
	var inst data.Instrument
	if err := s.db.GetDB().Where("instrument_id = ?", code).First(&inst).Error; err == nil {
		return inst.Name
	}
	if err := s.db.GetDB().Where("symbol = ?", symbolDigits(code)).First(&inst).Error; err == nil {
		return inst.Name
	}
	return ""
}

// symbolDigits 提取代码中的连续数字（纯证券代码），兼容 600519.SH / sh600519 等格式。
func symbolDigits(code string) string {
	var b []byte
	for i := 0; i < len(code); i++ {
		c := code[i]
		if c >= '0' && c <= '9' {
			b = append(b, c)
			if len(b) == 6 {
				break
			}
		}
	}
	return string(b)
}

// toOhlcSymbol 将持仓纯数字代码（instrument_id，如 002437）归一化为 stock.ohlc 的
// 带市场前缀小写 symbol（如 sz002437/sh600206/bj4xxxxxxx），保证 GetKlineFromStock 精确匹配命中。
// 市场推断规则与系统统一口径一致：6/5开头→沪，0/2/3开头→深，4/8头→北交所。代码已带前缀时原样小写返回。
func toOhlcSymbol(code string) string {
	c := strings.TrimSpace(code)
	lower := strings.ToLower(c)
	if len(lower) >= 2 {
		pre := lower[:2]
		if pre == "sh" || pre == "sz" || pre == "bj" {
			return lower
		}
	}
	num := symbolDigits(lower)
	if num == "" {
		return lower
	}
	switch {
	case strings.HasPrefix(num, "6"), strings.HasPrefix(num, "5"):
		return "sh" + num
	case strings.HasPrefix(num, "0"), strings.HasPrefix(num, "2"), strings.HasPrefix(num, "3"):
		return "sz" + num
	case strings.HasPrefix(num, "4"), strings.HasPrefix(num, "8"), strings.HasPrefix(num, "9"):
		return "bj" + num
	default:
		return "sh" + num
	}
}

// getHeldStockCodes 获取当前持仓股票代码列表（只统计实盘/模拟账户中开户且仍有数量的持仓）
func (s *Service) getHeldStockCodes() []string {
	db := s.db.GetDB()
	if db == nil {
		return nil
	}
	var codes []string
	if err := db.Model(&data.Position{}).
		Where("position_status = ? AND quantity > 0", "open").
		Distinct().
		Order("instrument_id").
		Pluck("instrument_id", &codes).Error; err != nil {
		log.Printf("[StrategyService] 查询持仓股票失败: %v", err)
		return nil
	}
	return codes
}

// getSampleBars 构建回测样本池：优先当前持仓股票，持仓为空时回退基准指数。
// 每个样本取较长历史K线（sampleLookback 根），在单标的上也能产生足量交易。
func (s *Service) getSampleBars(duckdbMgr *data.DuckDBManager) ([]sampleBars, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	var samples []sampleBars
	seen := make(map[string]bool)

	// 1. 当前持仓股票作为样本（真实成交、数量丰富）
	for _, code := range s.getHeldStockCodes() {
		// 持仓 instrument_id 为纯数字代码（如 002437），而 stock.ohlc 的 symbol 为带市场前缀的小写
		// 格式（如 sz002437/sh600206），需归一化后再精确匹配，否则一律匹配失败导致样本池回退到指数
		// （配对策略用指数对指数回归会退化为0成交、指标全0）。
		ohlcSymbol := toOhlcSymbol(code)
		if seen[ohlcSymbol] {
			continue
		}
		raw, err := duckdbMgr.GetKlineFromStock(ctx, ohlcSymbol, sampleLookback)
		if err != nil || len(raw) == 0 {
			log.Printf("[StrategyService] 持仓样本 %s(%s) 无K线数据，跳过: %v", code, ohlcSymbol, err)
			continue
		}
		samples = append(samples, sampleBars{Code: ohlcSymbol, Name: s.instrumentName(code), Bars: toAscKlineBars(raw)})
		seen[ohlcSymbol] = true
	}

	// 2. 持仓为空时回退基准指数（优先沪深300，回退上证指数）
	if len(samples) == 0 {
		for _, idx := range []string{"sh000300", "sh000001"} {
			raw, err := duckdbMgr.GetIndexKlineFromStock(ctx, idx, sampleLookback)
			if err != nil || len(raw) == 0 {
				log.Printf("[StrategyService] 基准指数 %s 不可用: %v", idx, err)
				continue
			}
			samples = append(samples, sampleBars{Code: idx, Bars: toAscKlineBars(raw)})
			seen[idx] = true
			break
		}
	}

	// 3. 若既无持仓也无基准指数，最终兜底返回空（调用方报错）
	if len(samples) == 0 {
		return nil, fmt.Errorf("无可用回测样本（无当前持仓股票且基准指数不可用）")
	}
	return samples, nil
}

// toAscKlineBars 将库K线（倒序：新在前）转换为升序 tdx.KlineBar
func toAscKlineBars(raw []data.KlineBarFromDuckDB) []tdx.KlineBar {
	result := make([]tdx.KlineBar, len(raw))
	for i, bar := range raw {
		result[i] = tdx.KlineBar{
			Date:   bar.Date.Format("2006-01-02"),
			Open:   bar.Open,
			High:   bar.High,
			Low:    bar.Low,
			Close:  bar.Close,
			Volume: int64(bar.Volume),
			Amount: bar.Amount,
		}
	}
	for i, j := 0, len(result)-1; i < j; i, j = i+1, j-1 {
		result[i], result[j] = result[j], result[i]
	}
	return result
}

// mean 计算浮点切片平均值（空切片返回0）
func mean(vals []float64) float64 {
	if len(vals) == 0 {
		return 0
	}
	sum := 0.0
	for _, v := range vals {
		sum += v
	}
	return sum / float64(len(vals))
}

// toDTO 转换为DTO
func (s *Service) toDTO(strategy *data.Strategy) *StrategyDTO {
	status := "stopped"
	if strategy.IsActive == 1 {
		status = "running"
	}

	return &StrategyDTO{
		ID:             strategy.ID,
		Name:           strategy.Name,
		Description:    strategy.Description,
		StrategyType:   strategy.StrategyType,
		Status:         status,
		IsBuiltin:      strategy.IsBuiltin == 1,
		SharpeRatio:    strategy.SharpeRatio,
		MaxDrawdown:    strategy.MaxDrawdown,
		TotalReturn:    strategy.TotalReturn,
		WinRate:        strategy.WinRate,
		Turnover:       strategy.Turnover,
		InitialCapital: strategy.InitialCapital,
		MaxPosition:    strategy.MaxPosition,
		StopLossPct:    strategy.StopLossPct,
		TakeProfitPct:  strategy.TakeProfitPct,
		CreatedAt:      strategy.CreatedAt.Format(time.RFC3339),
		UpdatedAt:      strategy.UpdatedAt.Format(time.RFC3339),
	}
}

// GetStrategyCount 获取策略数量
func (s *Service) GetStrategyCount() (int64, error) {
	db := s.db.GetDB()
	if db == nil {
		return 0, fmt.Errorf("数据库未初始化")
	}

	var count int64
	db.Model(&data.Strategy{}).Count(&count)
	return count, nil
}
