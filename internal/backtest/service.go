package backtest

import (
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/quantpilot/quantpilot/internal/data"
	"github.com/quantpilot/quantpilot/internal/tdx"
)

// Service 回测服务
type Service struct {
	db        *data.SQLiteManager
	tdxClient *tdx.TDXService
	duckdbMgr *data.DuckDBManager
}

// NewService 创建回测服务
func NewService(db *data.SQLiteManager) *Service {
	return &Service{db: db}
}

// SetTDXClient 设置TDX数据客户端
func (s *Service) SetTDXClient(client *tdx.TDXService) {
	s.tdxClient = client
}

// SetDuckDBManager 设置DuckDB管理器
func (s *Service) SetDuckDBManager(mgr *data.DuckDBManager) {
	s.duckdbMgr = mgr
}

// getTDXClient 获取或创建TDX客户端
func (s *Service) getTDXClient() (*tdx.TDXService, error) {
	if s.tdxClient != nil {
		return s.tdxClient, nil
	}

	client := tdx.NewTDXService()
	if err := client.Connect(); err != nil {
		return nil, fmt.Errorf("TDX连接失败: %w", err)
	}
	s.tdxClient = client
	return client, nil
}

// fetchBars 获取并过滤回测K线：自动识别市场 → 优先DuckDB离线数据 → 回退TDX → 日期过滤。
// 同时处理请求默认值（初始资金/周期/市场）与日期格式校验；供单次回测与参数网格扫描复用。
func (s *Service) fetchBars(req *RunBacktestRequest) ([]tdx.KlineBar, error) {
	if req.InitialCapital == 0 {
		req.InitialCapital = 100000
	}

	if req.Period == "" {
		req.Period = "day"
	}

	if req.Market == "" {
		// 尝试从股票代码自动识别市场
		detectedMarket := data.DetermineMarketFromSymbol(req.StockCode)
		if detectedMarket != "" {
			req.Market = detectedMarket
			log.Printf("[BacktestService] 自动检测市场: %s%s -> %s", req.Market, req.StockCode, req.Market)
		} else {
			req.Market = "SH" // 默认沪市
		}
	}

	// 请求参数校验：日期格式错误必须显式报错，不能静默忽略后当作无过滤条件
	for label, val := range map[string]string{"StartDate": req.StartDate, "EndDate": req.EndDate} {
		if val == "" {
			continue
		}
		if _, err := time.Parse("2006-01-02", val); err != nil {
			return nil, fmt.Errorf("回测参数校验失败: %s=%q 不是合法日期（格式应为 YYYY-MM-DD）", label, val)
		}
	}

	// 获取真实K线数据 - 优先使用DuckDB离线历史数据
	log.Printf("[BacktestService] Fetching market data: %s%s, period=%s", req.Market, req.StockCode, req.Period)
	var bars []tdx.KlineBar
	var err error

	if s.duckdbMgr != nil && s.duckdbMgr.HasStockDB() {
		// 构建完整的symbol：确保市场前缀正确
		stockCode := req.StockCode
		lowerCode := strings.ToLower(stockCode)
		if !strings.HasPrefix(lowerCode, "sh") && !strings.HasPrefix(lowerCode, "sz") && !strings.HasPrefix(lowerCode, "bj") {
			fullSymbol := strings.ToLower(req.Market) + stockCode
			bars, err = GetKlineFromDuckDB(s.duckdbMgr, fullSymbol, 500)
			log.Printf("[BacktestService] 查询DuckDB: stockCode=%s, fullSymbol=%s, result=%d条, err=%v",
				stockCode, fullSymbol, len(bars), err)
		} else {
			bars, err = GetKlineFromDuckDB(s.duckdbMgr, lowerCode, 500)
			log.Printf("[BacktestService] 查询DuckDB: stockCode=%s, 直接使用, result=%d条, err=%v",
				stockCode, len(bars), err)
		}
		if err == nil && bars != nil && len(bars) > 0 {
			log.Printf("[BacktestService] Got %d bars from DuckDB for %s%s", len(bars), req.Market, req.StockCode)
		} else {
			log.Printf("[BacktestService] DuckDB获取K线失败: %v, 尝试TDX", err)
		}
	} else {
		log.Printf("[BacktestService] DuckDB不可用，直接尝试TDX")
	}

	// 如果DuckDB获取失败或返回空，尝试TDX
	if bars == nil || len(bars) == 0 {
		tdxClient, tdxErr := s.getTDXClient()
		if tdxErr == nil {
			bars, err = tdxClient.GetKline(req.Market, req.StockCode, req.Period, 500)
			if err == nil && bars != nil && len(bars) > 0 {
				log.Printf("[BacktestService] Got %d bars from TDX for %s%s", len(bars), req.Market, req.StockCode)
			} else {
				log.Printf("[BacktestService] TDX获取K线失败或返回空: %v", err)
			}
		} else {
			log.Printf("[BacktestService] TDX连接失败: %v", tdxErr)
		}
	}

	if bars == nil || len(bars) == 0 {
		return nil, fmt.Errorf("无法获取 %s%s 的行情数据，请检查股票代码是否正确", req.Market, req.StockCode)
	}

	filtered := FilterBarsByDate(bars, req.StartDate, req.EndDate)
	if len(filtered) < 20 {
		return nil, fmt.Errorf("日期范围内K线数据不足（仅%d条），至少需要20条", len(filtered))
	}
	log.Printf("[BacktestService] After date filter: %d bars", len(filtered))
	return filtered, nil
}

// buildBacktestEngine 用统一口径构造回测引擎：ExecNextOpen（次日开盘成交、消除前视偏差）、
// 按标的自适应涨跌停幅度、按披露日注入真实财务数据。初始资金抬升等副作用同步回写 req，
// 保证数据库记录与前端展示的初始资金与回测实际口径一致。供单次回测与参数网格扫描复用。
func buildBacktestEngine(bars []tdx.KlineBar, strategy Strategy, req *RunBacktestRequest, dm *data.DuckDBManager) *BacktestEngine {
	engine := NewBacktestEngine(bars, strategy, req.InitialCapital)
	// 消除前视偏差：买卖信号生成后以次日开盘价成交，而非当日收盘价
	engine.ExecutionModel = ExecNextOpen
	// 按标的代码/名称自动推断A股涨跌停幅度（主板±10%、创业板/科创板±20%、北交所±30%、ST±5%）
	engine.LimitPct = LimitPctForSymbol(strings.ToLower(req.Market)+req.StockCode, req.StockName)
	// 注入财务数据提供者：基本面因子策略按披露日对齐读取真实财务数据（无未来函数）
	symFull := strings.ToLower(req.Market) + req.StockCode
	if provider := financialProviderFor(dm); provider != nil {
		engine.FinancialProvider = provider
		engine.FinancialSymbol = symFull
	}
	// 引擎内部会将不足以买1手高价股的初始资金自动抬升，同步回写请求保证口径一致
	if engine.InitialCapital != req.InitialCapital {
		log.Printf("[BacktestService] 初始资金由 %.0f 自动抬升至 %.0f（保证可买1手高价标的）",
			req.InitialCapital, engine.InitialCapital)
		req.InitialCapital = engine.InitialCapital
	}
	return engine
}

// BacktestResultDTO 回测结果数据传输对象
type BacktestResultDTO struct {
	ID             uint          `json:"id"`
	StrategyID     string        `json:"strategy_id"`
	StrategyName   string        `json:"strategy_name"`
	StrategyType   string        `json:"strategy_type"`
	StockCode      string        `json:"stock_code"`
	StockName      string        `json:"stock_name"`
	StartDate      string        `json:"start_date"`
	EndDate        string        `json:"end_date"`
	Period         string        `json:"period"`
	InitialCapital float64       `json:"initial_capital"`
	FinalCapital   float64       `json:"final_capital"`
	AnnualReturn   float64       `json:"annual_return"`
	SharpeRatio    float64       `json:"sharpe_ratio"`
	MaxDrawdown    float64       `json:"max_drawdown"`
	WinRate        float64       `json:"win_rate"`
	ProfitFactor   float64       `json:"profit_factor"`
	Turnover       float64       `json:"turnover"`
	TotalTrades    int           `json:"total_trades"`
	WinTrades      int           `json:"win_trades"`
	LossTrades     int           `json:"loss_trades"`
	BarsCount      int           `json:"bars_count"`
	Status         string        `json:"status"`
	ErrorMessage   string        `json:"error_message"`
	RunTime        string        `json:"run_time"`
	DurationMs     int           `json:"duration_ms"`
	CreatedAt      string        `json:"created_at"`
	Trades         []TradeRecord `json:"trades"`
	EquityCurve    []float64     `json:"equity_curve"`
	Dates          []string      `json:"dates"`
	BarData        []BarData     `json:"bar_data"`
	// 成本分解（三因子模型：佣金/印花税/市场冲击/收盘bias）
	TotalCommission float64   `json:"total_commission"`
	TotalStampDuty  float64   `json:"total_stamp_duty"`
	TotalImpact     float64   `json:"total_impact"`
	TotalBias       float64   `json:"total_bias"`
	TotalCost       float64   `json:"total_cost"`
	CostRatio       float64   `json:"cost_ratio"`
	CostPerTrade    float64   `json:"cost_per_trade"`
	CostCurve       []float64 `json:"cost_curve"`
}

// RunBacktestRequest 运行回测请求
type RunBacktestRequest struct {
	StrategyID     string  `json:"strategy_id"`
	StrategyName   string  `json:"strategy_name"`
	StrategyType   string  `json:"strategy_type"`
	StockCode      string  `json:"stock_code"`
	StockName      string  `json:"stock_name"`
	Market         string  `json:"market"`
	Period         string  `json:"period"`
	StartDate      string  `json:"start_date"`
	EndDate        string  `json:"end_date"`
	InitialCapital float64 `json:"initial_capital"`
	OperatorID     string  `json:"operator_id"`
	OperatorName   string  `json:"operator_name"`
}

// GetBacktestResultsRequest 获取回测结果请求
type GetBacktestResultsRequest struct {
	StrategyID string `json:"strategy_id"`
	Status     string `json:"status"`
	Limit      int    `json:"limit"`
	Offset     int    `json:"offset"`
}

// RunBacktest 运行回测
func (s *Service) RunBacktest(req *RunBacktestRequest) (*BacktestResultDTO, error) {
	if req == nil {
		return nil, fmt.Errorf("回测请求为空")
	}
	startTime := time.Now()

	db := s.db.GetDB()
	if db == nil {
		return nil, fmt.Errorf("数据库未初始化")
	}

	// 获取并过滤K线（自动识别市场/优先DuckDB/回退TDX/日期过滤），复用参数网格扫描同一数据入口
	bars, err := s.fetchBars(req)
	if err != nil {
		return nil, err
	}

	// 使用策略生成信号并执行回测
	strategy := GetStrategyByType(req.StrategyType)
	if strategy == nil {
		return nil, fmt.Errorf("未知的策略类型: %s", req.StrategyType)
	}

	log.Printf("[BacktestService] Running backtest: strategy_type=%s, strategy_name=%s, strategy_impl=%T",
		req.StrategyType, req.StrategyName, strategy)

	// 消除前视偏差（ExecNextOpen）、按标的自适应涨跌停、财务注入由 buildBacktestEngine 统一口径完成
	engine := buildBacktestEngine(bars, strategy, req, s.duckdbMgr)
	backtestResult := engine.Run()

	// 构建结果
	runTime := startTime
	durationMs := int(time.Since(startTime).Milliseconds())

	result := &data.BacktestResult{
		StrategyID:     req.StrategyID,
		StrategyName:   req.StrategyName,
		StrategyType:   req.StrategyType,
		StartDate:      backtestResult.StartDate,
		EndDate:        backtestResult.EndDate,
		InitialCapital: req.InitialCapital,
		FinalCapital:   backtestResult.FinalCapital,
		AnnualReturn:   backtestResult.AnnualReturn,
		SharpeRatio:    backtestResult.SharpeRatio,
		MaxDrawdown:    backtestResult.MaxDrawdown,
		WinRate:        backtestResult.WinRate,
		ProfitFactor:   backtestResult.ProfitFactor,
		TotalTrades:    backtestResult.TotalTrades,
		Status:         "COMPLETED",
		OperatorID:     req.OperatorID,
		OperatorName:   req.OperatorName,
		RunTime:        runTime,
		DurationMs:     durationMs,
	}

	config := map[string]interface{}{
		"strategy_id":     req.StrategyID,
		"strategy_name":   req.StrategyName,
		"strategy_type":   req.StrategyType,
		"stock_code":      req.StockCode,
		"stock_name":      req.StockName,
		"market":          req.Market,
		"period":          req.Period,
		"start_date":      req.StartDate,
		"end_date":        req.EndDate,
		"initial_capital": req.InitialCapital,
		"bars_count":      backtestResult.BarsCount,
	}
	configJSON, _ := json.Marshal(config)
	result.ConfigJSON = string(configJSON)

	log.Printf("[BacktestService] Backtest completed: strategy=%s, finalCapital=%.2f, annualReturn=%.2f%%, sharpe=%.4f, maxDD=%.2f%%, totalTrades=%d",
		req.StrategyName, backtestResult.FinalCapital, backtestResult.AnnualReturn, backtestResult.SharpeRatio,
		backtestResult.MaxDrawdown, backtestResult.TotalTrades)

	resultDetail := map[string]interface{}{
		"trades":        backtestResult.TotalTrades,
		"win_trades":    backtestResult.WinTrades,
		"loss_trades":   backtestResult.LossTrades,
		"annual":        backtestResult.AnnualReturn,
		"sharpe":        backtestResult.SharpeRatio,
		"max_dd":        backtestResult.MaxDrawdown,
		"win_rate":      backtestResult.WinRate,
		"profit_factor": backtestResult.ProfitFactor,
		"final_capital": backtestResult.FinalCapital,
	}
	resultJSON, _ := json.Marshal(resultDetail)
	result.ResultJSON = string(resultJSON)

	if err := db.Create(result).Error; err != nil {
		return nil, fmt.Errorf("保存回测结果失败: %w", err)
	}

	log.Printf("[BacktestService] Backtest completed: strategy=%s, stock=%s%s, return=%.2f%%, sharpe=%.2f, trades=%d, duration=%dms",
		req.StrategyName, req.Market, req.StockCode, backtestResult.AnnualReturn,
		backtestResult.SharpeRatio, backtestResult.TotalTrades, durationMs)

	dto := s.toDTO(result)
	dto.StockCode = req.StockCode
	dto.StockName = req.StockName
	dto.Period = req.Period
	dto.WinTrades = backtestResult.WinTrades
	dto.LossTrades = backtestResult.LossTrades
	dto.BarsCount = backtestResult.BarsCount
	dto.Trades = backtestResult.Trades
	dto.EquityCurve = backtestResult.EquityCurve
	dto.Dates = backtestResult.Dates
	dto.BarData = toBarData(bars)
	dto.Turnover = backtestResult.Turnover
	// 成本分解（三因子模型）透传前端
	dto.TotalCommission = backtestResult.TotalCommission
	dto.TotalStampDuty = backtestResult.TotalStampDuty
	dto.TotalImpact = backtestResult.TotalImpact
	dto.TotalBias = backtestResult.TotalBias
	dto.TotalCost = backtestResult.TotalCost
	dto.CostRatio = backtestResult.CostRatio
	dto.CostPerTrade = backtestResult.CostPerTrade
	dto.CostCurve = backtestResult.CostCurve

	return dto, nil
}

// GetBacktestResults 获取回测结果列表
func (s *Service) GetBacktestResults(req *GetBacktestResultsRequest) ([]*BacktestResultDTO, int64, error) {
	db := s.db.GetDB()
	if db == nil {
		return nil, 0, fmt.Errorf("数据库未初始化")
	}

	var results []data.BacktestResult
	var totalCount int64

	query := db.Model(&data.BacktestResult{})

	if req.StrategyID != "" {
		query = query.Where("strategy_id = ?", req.StrategyID)
	}
	if req.Status != "" {
		query = query.Where("status = ?", req.Status)
	}

	query.Count(&totalCount)

	if req.Limit > 0 {
		query = query.Limit(req.Limit)
	}
	if req.Offset > 0 {
		query = query.Offset(req.Offset)
	}

	if err := query.Order("run_time DESC").Find(&results).Error; err != nil {
		return nil, 0, fmt.Errorf("获取回测结果失败: %w", err)
	}

	result := make([]*BacktestResultDTO, len(results))
	for i, r := range results {
		result[i] = s.toDTO(&r)
	}

	return result, totalCount, nil
}

// GetBacktestResult 获取单个回测结果
func (s *Service) GetBacktestResult(id uint) (*BacktestResultDTO, error) {
	db := s.db.GetDB()
	if db == nil {
		return nil, fmt.Errorf("数据库未初始化")
	}

	var result data.BacktestResult
	if err := db.First(&result, id).Error; err != nil {
		return nil, fmt.Errorf("回测结果不存在: %d", id)
	}

	return s.toDTO(&result), nil
}

// DeleteBacktestResult 删除回测结果
func (s *Service) DeleteBacktestResult(id uint) error {
	db := s.db.GetDB()
	if db == nil {
		return fmt.Errorf("数据库未初始化")
	}

	result := db.Delete(&data.BacktestResult{}, id)
	if result.Error != nil {
		return fmt.Errorf("删除回测结果失败: %w", result.Error)
	}

	log.Printf("[BacktestService] Deleted backtest result: ID %d", id)
	return nil
}

// GetBacktestStats 获取回测统计信息
func (s *Service) GetBacktestStats() (map[string]interface{}, error) {
	db := s.db.GetDB()
	if db == nil {
		return nil, fmt.Errorf("数据库未初始化")
	}

	var totalRuns int64
	var completedRuns int64
	var failedRuns int64
	var avgReturn float64
	var bestReturn float64

	db.Model(&data.BacktestResult{}).Count(&totalRuns)
	db.Model(&data.BacktestResult{}).Where("status = ?", "COMPLETED").Count(&completedRuns)
	db.Model(&data.BacktestResult{}).Where("status = ?", "FAILED").Count(&failedRuns)

	if completedRuns > 0 {
		db.Model(&data.BacktestResult{}).
			Where("status = ?", "COMPLETED").
			Select("COALESCE(AVG(annual_return), 0)").
			Scan(&avgReturn)

		db.Model(&data.BacktestResult{}).
			Where("status = ?", "COMPLETED").
			Select("COALESCE(MAX(annual_return), 0)").
			Scan(&bestReturn)
	}

	return map[string]interface{}{
		"total_runs":     totalRuns,
		"completed_runs": completedRuns,
		"failed_runs":    failedRuns,
		"avg_return":     avgReturn,
		"best_return":    bestReturn,
	}, nil
}

// SaveBacktestResult 保存回测结果
func (s *Service) SaveBacktestResult(result *data.BacktestResult) error {
	db := s.db.GetDB()
	if db == nil {
		return fmt.Errorf("数据库未初始化")
	}

	if err := db.Create(result).Error; err != nil {
		return fmt.Errorf("保存回测结果失败: %w", err)
	}

	log.Printf("[BacktestService] Saved backtest result: strategy=%s", result.StrategyName)
	return nil
}

// toDTO 转换为DTO
func (s *Service) toDTO(result *data.BacktestResult) *BacktestResultDTO {
	return &BacktestResultDTO{
		ID:             result.ID,
		StrategyID:     result.StrategyID,
		StrategyName:   result.StrategyName,
		StrategyType:   result.StrategyType,
		StartDate:      result.StartDate,
		EndDate:        result.EndDate,
		InitialCapital: result.InitialCapital,
		FinalCapital:   result.FinalCapital,
		AnnualReturn:   result.AnnualReturn,
		SharpeRatio:    result.SharpeRatio,
		MaxDrawdown:    result.MaxDrawdown,
		WinRate:        result.WinRate,
		ProfitFactor:   result.ProfitFactor,
		TotalTrades:    result.TotalTrades,
		Status:         result.Status,
		ErrorMessage:   result.ErrorMessage,
		RunTime:        result.RunTime.Format(time.RFC3339),
		DurationMs:     result.DurationMs,
		CreatedAt:      result.CreatedAt.Format(time.RFC3339),
	}
}
