package portfolio

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/quantpilot/quantpilot/internal/backtest"
	"github.com/quantpilot/quantpilot/internal/data"
	"github.com/quantpilot/quantpilot/internal/orderbook"
	"github.com/quantpilot/quantpilot/internal/util"
	"gorm.io/gorm"
)

// TradingMode 交易模式
const (
	TradingModeSimulated = "simulated" // 模拟接口：将交易结果输出到JSON文件
	TradingModeLive      = "live"      // 实盘交易：按照券商接口要求生成交易指令JSON
)

// quoteCacheTTL 实时行情缓存有效期
// 页面加载/连续监控会频繁调用 RefreshPrices，短 TTL 缓存避免同一时刻重复网络请求
const quoteCacheTTL = 15 * time.Second

// getTradeOutputDir 获取交易输出目录
// 仅使用可执行文件目录下的data/trades文件夹
func getTradeOutputDir() string {
	exePath, err := os.Executable()
	if err != nil {
		panic(fmt.Sprintf("无法获取可执行文件路径: %v", err))
	}
	return filepath.Join(filepath.Dir(exePath), "data", "trades")
}

// Engine 组合交易引擎
type Engine struct {
	db           *data.SQLiteManager
	mu           sync.RWMutex
	positions    map[string]*PositionState
	cash         float64
	totalCapital float64
	// reservedBuy 待确认买入已占用资金（模拟接口模式下，操盘手决定买入后、用户确认前占用，
	// 避免并发决策重复分配同一笔资金。成交后释放占用并扣现金，拒绝/失败则释放。）
	reservedBuy float64
	portfolioID uint
	running     bool
	stopCh      chan struct{}
	tradingMode string // 交易模式：simulated 或 live
	outputDir   string // JSON输出目录

	// 实时行情缓存（短 TTL，避免页面加载/监控重复网络请求）
	quoteCache     []data.StockSnapshot
	quoteCacheTime time.Time

	// PrevClose 缓存（避免每次 GetSnapshot 都查询数据库）
	prevCloseCache       map[string]float64 // instrumentID -> prevClose
	prevCloseCacheLoaded bool               // 是否已加载缓存

	// 批量保存队列（减少数据库写入次数）
	pendingSaves      []string      // 待保存的 instrumentID
	lastSaveTime      time.Time     // 上次保存时间
	saveBatchInterval time.Duration // 批量保存间隔

	// autoReportMonth 已自动生成月度报告的月份（YYYY-MM），避免同一月份重复生成
	autoReportMonth string

	// emergencyGate 紧急停止门控：返回 true 时禁止一切买卖成交。
	// 由 App 接线到 Policy 紧急停止/CIO 智能体停止状态，作为最终执行层防线，
	// 确保紧急停止后即使绕过 CIO 编排/审批补确认等路径，也无法产生任何真实成交。
	emergencyGate func() bool

	// buyGate 强制暂停建仓门控：返回 true 时禁止一切买入成交（卖出不受影响）。
	// 由 App 接线到 Policy 暂停建仓开关，作为买入最终执行层防线。
	buyGate func() bool
}

// SetEmergencyGate 设置紧急停止门控（nil 表示不启用）
func (e *Engine) SetEmergencyGate(fn func() bool) {
	e.emergencyGate = fn
}

// SetBuyGate 设置强制暂停建仓门控（nil 表示不启用）。注意：buyGate 仅拦截买入，
// 不影响卖出，从而实现"强制暂停建仓但允许离场/止损"的业务诉求。
func (e *Engine) SetBuyGate(fn func() bool) {
	e.buyGate = fn
}

// emergencyStopped 判断当前是否处于紧急停止状态
func (e *Engine) emergencyStopped() bool {
	if e.emergencyGate == nil {
		return false
	}
	return e.emergencyGate()
}

// buyStopped 判断当前是否处于强制暂停建仓状态
func (e *Engine) buyStopped() bool {
	if e.buyGate == nil {
		return false
	}
	return e.buyGate()
}

// PositionState 内存中的持仓状态
type PositionState struct {
	InstrumentID        string    `json:"instrument_id"`
	StockName           string    `json:"stock_name"`
	Market              string    `json:"market"`
	Quantity            int       `json:"quantity"`
	AvgCost             float64   `json:"avg_cost"`
	CurrentPrice        float64   `json:"current_price"`
	PrevClose           float64   `json:"prev_close"`
	MarketValue         float64   `json:"market_value"`
	UnrealizedPnL       float64   `json:"unrealized_pnl"`
	UnrealizedReturn    float64   `json:"unrealized_return"`
	Weight              float64   `json:"weight"`
	OpenDate            time.Time `json:"open_date"`
	LastUpdate          time.Time `json:"last_update"`
	TodayBoughtQuantity int       `json:"today_bought_quantity"` // 今日买入数量（T+1：当日买入不可卖出）
}

// TradeRecord 交易记录（用于API返回）
type TradeRecord struct {
	TradeID      string    `json:"trade_id"`
	Side         string    `json:"side"`
	InstrumentID string    `json:"instrument_id"`
	StockName    string    `json:"stock_name"`
	Quantity     int       `json:"quantity"`
	Price        float64   `json:"price"`
	GrossAmount  float64   `json:"gross_amount"`
	Commission   float64   `json:"commission"`
	Fees         float64   `json:"fees"`
	NetAmount    float64   `json:"net_amount"`
	RealizedPnL  float64   `json:"realized_pnl"`
	DecisionID   string    `json:"decision_id"`
	TradeDate    time.Time `json:"trade_date"`
	Reason       string    `json:"reason"`
}

// PortfolioSnapshot 组合快照
type PortfolioSnapshot struct {
	PortfolioID      uint             `json:"portfolio_id"`
	TotalCapital     float64          `json:"total_capital"`
	Cash             float64          `json:"cash"`
	ReservedCash     float64          `json:"reserved_cash"`  // 待确认买入已占用资金
	AvailableCash    float64          `json:"available_cash"` // 可自由支配现金 = cash - reserved_cash
	TotalMarketValue float64          `json:"total_market_value"`
	TotalPnL         float64          `json:"total_pnl"`
	TotalReturn      float64          `json:"total_return"`
	Positions        []*PositionState `json:"positions"`
	LastUpdated      time.Time        `json:"last_updated"`
	DailyPnL         float64          `json:"daily_pnl"`
	DailyReturn      float64          `json:"daily_return"`
}

// NewEngine 创建组合交易引擎
func NewEngine(db *data.SQLiteManager, initialCapital float64) (*Engine, error) {
	if db == nil {
		return nil, fmt.Errorf("database not initialized")
	}

	// 默认输出目录：优先使用exe目录/data/trades
	outputDir := getTradeOutputDir()
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		log.Printf("[Portfolio] Warning: failed to create output directory %s: %v, using temp dir", outputDir, err)
		outputDir = os.TempDir()
	}

	engine := &Engine{
		db:                db,
		positions:         make(map[string]*PositionState),
		cash:              initialCapital,
		totalCapital:      initialCapital,
		stopCh:            make(chan struct{}),
		tradingMode:       TradingModeSimulated,
		outputDir:         outputDir,
		prevCloseCache:    make(map[string]float64),
		lastSaveTime:      time.Now(),
		saveBatchInterval: 5 * time.Second, // 批量保存间隔 5 秒
	}

	if err := engine.initPortfolio(); err != nil {
		log.Printf("[Portfolio] initPortfolio failed: %v", err)
		return nil, err
	}

	log.Printf("[Portfolio] Engine created successfully: initialCapital=%.2f, portfolioID=%d",
		initialCapital, engine.portfolioID)

	return engine, nil
}

// SetTradingMode 设置交易模式
func (e *Engine) SetTradingMode(mode string) {
	e.tradingMode = mode
	log.Printf("[Portfolio] Trading mode set to: %s", mode)
}

// GetTradingMode 获取当前交易模式
func (e *Engine) GetTradingMode() string {
	return e.tradingMode
}

// SetOutputDir 设置JSON输出目录
func (e *Engine) SetOutputDir(dir string) {
	e.outputDir = dir
	os.MkdirAll(dir, 0755)
}

// initPortfolio 初始化组合（从数据库加载或创建新组合）
func (e *Engine) initPortfolio() error {
	if e.db == nil {
		return fmt.Errorf("database not initialized")
	}

	log.Printf("[Portfolio] initPortfolio start: totalCapital=%.2f, cash=%.2f", e.totalCapital, e.cash)

	// 查找或创建组合
	var portfolio data.Portfolio
	result := e.db.GetDB().Where("name = ?", "AI量化小基金").First(&portfolio)
	if result.Error != nil {
		log.Printf("[Portfolio] Portfolio query error: %v, isRecordNotFound=%v", result.Error, errors.Is(result.Error, gorm.ErrRecordNotFound))
		if !errors.Is(result.Error, gorm.ErrRecordNotFound) {
			return fmt.Errorf("failed to query portfolio: %w", result.Error)
		}
		// 组合不存在，创建新组合
		log.Printf("[Portfolio] Portfolio not found, creating new one (initial capital: %.2f)", e.totalCapital)
		portfolio = data.Portfolio{
			Name:           "AI量化小基金",
			PortfolioType:  "MANAGED",
			InitialCapital: e.totalCapital,
			CurrentCapital: e.totalCapital,
			Cash:           e.cash,
			IsActive:       1,
		}
		if err := e.db.GetDB().Create(&portfolio).Error; err != nil {
			log.Printf("[Portfolio] Failed to create portfolio: %v", err)
			return fmt.Errorf("failed to create portfolio: %w", err)
		}
		log.Printf("[Portfolio] New portfolio created: ID=%d", portfolio.ID)
	}

	e.portfolioID = portfolio.ID
	// 期初资金以配置的初始资金为准（NewEngine 传入，此时 e.totalCapital 尚未被 CurrentCapital 覆盖）：
	// 若数据库中的 InitialCapital 与配置不一致（例如旧版本未在 Reset 时同步数据库），
	// 在此自动纠正，避免累计盈亏 = 总资产 - 旧期初资金 的失真显示（如改初始资金为 0 后仍显示 -10 万）。
	if abs1(portfolio.InitialCapital-e.totalCapital) > 0.01 {
		if err := e.db.GetDB().Model(&data.Portfolio{}).
			Where("id = ?", e.portfolioID).
			Updates(map[string]interface{}{"initial_capital": e.totalCapital}).Error; err != nil {
			log.Printf("[Portfolio] Warning: failed to sync initial_capital %.2f -> %.2f: %v",
				portfolio.InitialCapital, e.totalCapital, err)
		} else {
			log.Printf("[Portfolio] InitialCapital synced: %.2f -> %.2f (from config)",
				portfolio.InitialCapital, e.totalCapital)
			portfolio.InitialCapital = e.totalCapital
		}
	}
	e.cash = portfolio.Cash
	e.totalCapital = portfolio.CurrentCapital
	log.Printf("[Portfolio] Portfolio loaded: ID=%d, Cash=%.2f, TotalCapital=%.2f", e.portfolioID, e.cash, e.totalCapital)

	// 加载现有持仓
	var positions []data.Position
	posResult := e.db.GetDB().Where("portfolio_id = ? AND position_status = ?", e.portfolioID, "open").Find(&positions)
	if posResult.Error != nil {
		log.Printf("[Portfolio] Warning: failed to load positions: %v", posResult.Error)
	} else {
		log.Printf("[Portfolio] Loaded %d positions from database", len(positions))
	}

	// 批量查询所有持仓的最新快照（避免逐持仓 N+1 查询）
	latestSnapshotByInstrument := make(map[string]data.PositionSnapshot, len(positions))
	if len(positions) > 0 {
		instrumentIDs := make([]string, 0, len(positions))
		for _, p := range positions {
			instrumentIDs = append(instrumentIDs, p.InstrumentID)
		}
		var allSnapshots []data.PositionSnapshot
		e.db.GetDB().
			Where("portfolio_id = ? AND instrument_id IN ?", e.portfolioID, instrumentIDs).
			Order("snapshot_date DESC").
			Find(&allSnapshots)
		// Order DESC 保证每个 instrument 第一次出现即最新快照
		for _, snap := range allSnapshots {
			if _, ok := latestSnapshotByInstrument[snap.InstrumentID]; !ok {
				latestSnapshotByInstrument[snap.InstrumentID] = snap
			}
		}
	}

	// T+1 防绕过：进程重启后内存无 TodayBoughtQuantity，改为从当日成交记录重建，
	// 否则当日买入的持仓在重启后会被当成"昨日持仓"而允许卖出（A股当日买入次日才能卖）。
	todayBoughtQty := make(map[string]int, len(positions))
	if len(positions) > 0 && util.IsTradingDay(time.Now()) {
		instIDs := make([]string, 0, len(positions))
		for _, p := range positions {
			instIDs = append(instIDs, p.InstrumentID)
		}
		// 集合：当日所有 BUY 成交（含当日买卖的持仓，需一并统计以防被卖）
		var buys []data.Trade
		startOfDay := time.Date(time.Now().Year(), time.Now().Month(), time.Now().Day(), 0, 0, 0, 0, time.Now().Location())
		e.db.GetDB().
			Where("portfolio_id = ? AND instrument_id IN ? AND side = ? AND trade_date >= ?",
				e.portfolioID, instIDs, "BUY", startOfDay).
			Find(&buys)
		for _, t := range buys {
			todayBoughtQty[t.InstrumentID] += t.Quantity
		}
		log.Printf("[Portfolio] T+1今日买入量重建完成: %d 只持仓有当日买入", len(todayBoughtQty))
	}

	for _, p := range positions {
		// 从字典获取股票名称（尝试多种方式）
		stockName := data.GetDictLoader().GetStockName(p.InstrumentID)

		// 如果返回的是原始代码，说明查找失败，尝试带市场前缀查找
		if stockName == p.InstrumentID {
			// 尝试不同市场前缀
			prefixes := []string{"sh", "sz", "bj", "SH", "SZ", "BJ"}
			for _, prefix := range prefixes {
				name := data.GetDictLoader().GetStockName(prefix + p.InstrumentID)
				if name != p.InstrumentID && name != "" {
					stockName = name
					break
				}
			}
		}

		// 如果还是找不到，使用代码作为后备
		if stockName == "" || stockName == p.InstrumentID {
			stockName = p.InstrumentID
		}

		// 判断市场（沪市/深市/北交所）
		market := "SH"
		codeLen := len(p.InstrumentID)
		if codeLen >= 6 {
			prefix := p.InstrumentID[:3]
			if prefix == "000" || prefix == "001" || prefix == "002" || prefix == "003" ||
				prefix == "200" || prefix == "300" || prefix == "301" {
				market = "SZ" // 深市主板、中小板、创业板
			} else if prefix == "430" || prefix == "831" || prefix == "870" || prefix == "871" || prefix == "872" || prefix == "873" {
				market = "BJ" // 北交所
			} else {
				market = "SH" // 沪市主板、科创板
			}
		}

		// 尝试从最新的持仓快照获取PrevClose
		// 优先使用前一日快照的ClosePrice作为今日的PrevClose
		prevClose := 0.0
		if latestSnapshot, ok := latestSnapshotByInstrument[p.InstrumentID]; ok {
			// 优先使用快照的ClosePrice（前一日收盘价）作为PrevClose
			if latestSnapshot.ClosePrice > 0 {
				prevClose = latestSnapshot.ClosePrice
			} else if latestSnapshot.PrevClose > 0 {
				// 回退：如果ClosePrice为0，使用快照的PrevClose
				prevClose = latestSnapshot.PrevClose
			}
		}

		// 如果PrevClose还是0，使用CurrentPrice作为初始值（日内盈亏为0）
		if prevClose <= 0 && p.CurrentPrice > 0 {
			prevClose = p.CurrentPrice
		}

		// 跳过数量为0的持仓（已清仓）
		if p.Quantity <= 0 {
			log.Printf("[Portfolio] Skipping zero-quantity position: %s (quantity=%d)", p.InstrumentID, p.Quantity)
			continue
		}

		pos := &PositionState{
			InstrumentID:     p.InstrumentID,
			StockName:        stockName,
			Market:           market,
			Quantity:         p.Quantity,
			AvgCost:          p.AvgCost,
			CurrentPrice:     p.CurrentPrice,
			PrevClose:        prevClose,
			MarketValue:      p.MarketValue,
			UnrealizedPnL:    p.UnrealizedPnL,
			UnrealizedReturn: p.UnrealizedReturn,
			// T+1：从当日成交记录重建今日买入量，防止重启后当日买入被误判为可卖
			TodayBoughtQuantity: todayBoughtQty[p.InstrumentID],
			LastUpdate:          time.Now(),
		}
		e.positions[p.InstrumentID] = pos
	}

	// 以 sqlite 成交记录为准对账持仓数量：成交不再漂移，即使 positions.quantity 与成交不一致也强制校正。
	e.reconcilePositionsFromTrades()

	log.Printf("[Portfolio] Engine initialized: portfolio=%d, cash=%.2f, positions=%d",
		e.portfolioID, e.cash, len(e.positions))

	return nil
}

// reconcilePositionsFromTrades 以 sqlite trades 成交记录为唯一权威，重算各持仓净数量。
// 净数量 = Σ(BUY量) - Σ(SELL量)，覆盖内存 e.positions 与 DB positions 的 quantity，
// 保证前端任何显示都以真实成交为准，不残留 positions.quantity 漂移（如卖出后仍显示旧数量）。
// 清除净数量 <=0 的持仓（全额卖出/清仓），并将最末成交时间作为 LastUpdate。
func (e *Engine) reconcilePositionsFromTrades() {
	if e.db == nil {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	var trades []data.Trade
	// 全持仓范围内一次性取当天与历史成交（历史全量因组合累积成交可控）
	if err := e.db.GetDB().Where("portfolio_id = ?", e.portfolioID).Find(&trades).Error; err != nil {
		log.Printf("[Portfolio] reconcilePositionsFromTrades: 读取成交失败: %v", err)
		return
	}
	// 平均成本与最新成交时间（全量，重建缺失持仓用）
	avgCost := make(map[string]float64)
	buyAmt := make(map[string]float64)
	buyQty := make(map[string]int)
	lastTrade := make(map[string]time.Time)
	for _, t := range trades {
		if t.Side == "BUY" {
			buyAmt[t.InstrumentID] += float64(t.Quantity) * t.Price
			buyQty[t.InstrumentID] += t.Quantity
		}
		if t.TradeDate.After(lastTrade[t.InstrumentID]) {
			lastTrade[t.InstrumentID] = t.TradeDate
		}
	}
	for id, denom := range buyQty {
		if denom != 0 {
			avgCost[id] = buyAmt[id] / float64(denom)
		}
	}

	// 当前净持仓：以 position_snapshots 最近收盘快照为权威基准，叠加快照之后的新成交。
	// 仅依赖历史交易净量(ΣBUY−ΣSELL)时，历史幽灵买单/卖单虽相互抵消，但也可能在跨日更新中
	// 误重建「昨日已清仓」标的的持仓；以收盘快照为准可确保已清仓标的不会被历史漂移买单一并重建。
	netQty := make(map[string]int)
	var latestDate string
	e.db.GetDB().Raw("SELECT MAX(snapshot_date) FROM position_snapshots WHERE portfolio_id = ?", e.portfolioID).Scan(&latestDate)
	if latestDate != "" {
		var snaps []data.PositionSnapshot
		e.db.GetDB().Where("portfolio_id = ? AND snapshot_date = ?", e.portfolioID, latestDate).Find(&snaps)
		for _, s := range snaps {
			if s.Quantity > 0 {
				netQty[s.InstrumentID] = s.Quantity
			}
		}
		// 快照日 23:59:59.999 之后的成交才叠加到基准之上
		baseEnd, _ := time.Parse("2006-01-02", latestDate)
		baseEnd = baseEnd.Add(24*time.Hour - time.Nanosecond)
		for _, t := range trades {
			if t.TradeDate.After(baseEnd) {
				q := t.Quantity
				if t.Side == "SELL" {
					q = -q
				}
				netQty[t.InstrumentID] += q
			}
		}
	} else {
		// 无任何快照时回退到全历史成交净量
		for _, t := range trades {
			q := t.Quantity
			if t.Side == "SELL" {
				q = -q
			}
			netQty[t.InstrumentID] += q
		}
	}

	// 同步 DB positions.quantity 与 内存持仓
	for id, qty := range netQty {
		if qty <= 0 {
			// 净持仓 <=0：视为已清仓，从内存删除并置 DB closed
			if _, ok := e.positions[id]; ok {
				delete(e.positions, id)
			}
			e.db.GetDB().Model(&data.Position{}).
				Where("portfolio_id = ? AND instrument_id = ?", e.portfolioID, id).
				Updates(map[string]interface{}{"quantity": 0, "position_status": "closed", "updated_at": time.Now()})
			continue
		}
		if pos, ok := e.positions[id]; ok {
			pos.Quantity = qty
			if !lastTrade[id].IsZero() {
				pos.LastUpdate = lastTrade[id]
			}
			e.updatePositionMetrics(pos)
			// 立即写回 DB，保证 sqlite 与实际成交一致（含市值/浮动盈亏，避免 positions 表残留旧价旧市值）
			e.db.GetDB().Model(&data.Position{}).
				Where("portfolio_id = ? AND instrument_id = ? AND position_status = ?", e.portfolioID, id, "open").
				Updates(map[string]interface{}{
					"quantity":       qty,
					"avg_cost":       pos.AvgCost,
					"current_price":  pos.CurrentPrice,
					"market_value":   pos.MarketValue,
					"unrealized_pnl": pos.UnrealizedPnL,
					"updated_at":     time.Now(),
				})
			continue
		}
		// —— 修复：trades 有净持仓(>0)，但内存/DB 无该 open 持仓（丢弃丢失/未重建）——
		// 重建持仓并立即写回 DB open，保证前端持仓与交易记录一致。
		ac := avgCost[id]
		if ac <= 0 {
			ac = 0
		}
		lt := lastTrade[id]
		// T+1：重建持仓时从当日 BUY 成交恢复今日买入量，防止对账重建后当日买入被误判为可卖
		todayBought := 0
		if util.IsTradingDay(time.Now()) {
			startOfDay := time.Date(time.Now().Year(), time.Now().Month(), time.Now().Day(), 0, 0, 0, 0, time.Now().Location())
			for _, t := range trades {
				if t.InstrumentID == id && t.Side == "BUY" && !t.TradeDate.Before(startOfDay) {
					todayBought += t.Quantity
				}
			}
		}
		newPos := &PositionState{
			InstrumentID:        id,
			Quantity:            qty,
			AvgCost:             ac,
			CurrentPrice:        ac, // 即时股价为例外字段，随后由行情更新
			TodayBoughtQuantity: todayBought,
			OpenDate:            lt,
			LastUpdate:          lt,
		}
		e.positions[id] = newPos
		// 写 DB open 行（幂等：不存在则插入，存在则重置为 open）
		var cnt int64
		e.db.GetDB().Model(&data.Position{}).
			Where("portfolio_id = ? AND instrument_id = ?", e.portfolioID, id).Count(&cnt)
		if cnt > 0 {
			e.db.GetDB().Model(&data.Position{}).
				Where("portfolio_id = ? AND instrument_id = ?", e.portfolioID, id).
				Updates(map[string]interface{}{
					"quantity":        qty,
					"avg_cost":        ac,
					"current_price":   ac,
					"market_value":    ac * float64(qty),
					"unrealized_pnl":  0,
					"position_status": "open",
					"updated_at":      time.Now(),
				})
		} else {
			e.db.GetDB().Create(&data.Position{
				PortfolioID:      e.portfolioID,
				InstrumentID:     id,
				Quantity:         qty,
				AvgCost:          ac,
				CurrentPrice:     ac,
				MarketValue:      ac * float64(qty),
				UnrealizedPnL:    0,
				UnrealizedReturn: 0,
				Weight:           0,
				PositionStatus:   "open",
			})
		}
		log.Printf("[Portfolio] 对账重建缺失持仓: %s 净%d股 成本¥%.2f (原positions无该open行)", id, qty, ac)
	}
	log.Printf("[Portfolio] 持仓对账完成: 净持仓 %d 只 (以成交记录为准)", len(netQty))

	// 现金对账：以交易明细账(trades)为唯一权威推导现金余额，杜绝内存/DB 现金漂移。
	// 现金 = 期初资金 + Σ(卖出净额) − Σ(买入净额)；必须持有 e.mu 锁，故在此函数尾部调用。
	e.reconcileCashFromTradesLocked()
}

// recordCashFlow 记录一条资金流水（账本闭环：每笔现金变动独立留痕）。
// 调用方须已持有 e.mu 锁（与现金变动同一临界区）；BalanceAfter 使用当时内存 e.cash。
func (e *Engine) recordCashFlow(orderID, tradeID, instrumentID, cfType, side string, amount, netAmount float64, reason string) {
	if e.db == nil || e.db.GetDB() == nil {
		return
	}
	cf := data.CashFlow{
		PortfolioID:  e.portfolioID,
		TradeID:      tradeID,
		OrderID:      orderID,
		InstrumentID: instrumentID,
		Type:         cfType,
		Side:         side,
		Amount:       amount,
		NetAmount:    netAmount,
		BalanceAfter: e.cash,
		Reason:       reason,
		CreatedAt:    time.Now(),
	}
	if err := e.db.GetDB().Create(&cf).Error; err != nil {
		log.Printf("[Portfolio] 资金流水记录失败 %s %s: %v", cfType, instrumentID, err)
	}
}

// reconcileCashFromTradesLocked 以交易明细账(trades)为唯一权威推导现金余额（财务流水账：余额由流水推导）。
// 现金 = 期初资金 + Σ(卖出净额) − Σ(买入净额)（NetAmount 已含佣金/税费），
// 覆盖内存 e.cash 与 DB portfolios.cash，任何时刻前端显示的现金都以真实成交流水为准。
// 待确认买入占用(reservedBuy)属「临时账」不在此重算：挂单未成交不进入 trades，成交后才经 Buy 扣减现金并释放占用。
// 调用方必须已持有 e.mu 锁（仅 reconcilePositionsFromTrades 尾部调用）。
func (e *Engine) reconcileCashFromTradesLocked() {
	if e.db == nil {
		return
	}
	var trades []data.Trade
	if err := e.db.GetDB().Where("portfolio_id = ?", e.portfolioID).Find(&trades).Error; err != nil {
		log.Printf("[Portfolio] 现金对账: 读取成交失败: %v", err)
		return
	}
	opening := e.getInitialCapital()
	cash := opening
	for _, t := range trades {
		switch t.Side {
		case "BUY":
			cash -= t.NetAmount
		case "SELL":
			cash += t.NetAmount
		}
	}
	old := e.cash
	e.cash = cash
	if err := e.db.GetDB().Model(&data.Portfolio{}).Where("id = ?", e.portfolioID).Update("cash", cash).Error; err != nil {
		log.Printf("[Portfolio] 现金对账写库失败: %v", err)
	}
	log.Printf("[Portfolio] 现金对账: %.2f -> %.2f (期初%.2f, 明细账成交%d笔)", old, cash, opening, len(trades))
}

// ReconcileForSettlement 日终结算持仓校对：
// 以 sqlite 成交记录（ΣBUY−ΣSELL，叠加最近收盘快照）为准对账持仓数量与平均成本，
// 修正内存及 positions 表的数量漂移，确保结算快照与收益率/净值计算基于一致数据。
func (e *Engine) ReconcileForSettlement() error {
	e.reconcilePositionsFromTrades()
	return nil
}

// ReserveBuy 为待确认买入占用资金。
// 仅当「可用现金」足以覆盖该笔买入预占用时才占用，否则返回错误（资金超占用，避免超买）。
func (e *Engine) ReserveBuy(amount float64) error {
	if amount <= 0 {
		return nil
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	avail := e.cash - e.reservedBuy
	if avail < amount {
		return fmt.Errorf("可用资金不足，无法占用买入: 需¥%.2f, 可用¥%.2f", amount, avail)
	}
	e.reservedBuy += amount
	return nil
}

// ReleaseBuyReserve 释放待确认买入已占用资金（订单拒绝/失败/取消时调用）。
func (e *Engine) ReleaseBuyReserve(amount float64) {
	if amount <= 0 {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.reservedBuy -= amount
	if e.reservedBuy < 0 {
		e.reservedBuy = 0
	}
}

// GetPortfolioID 返回组合ID（供订单临时表落库时关联）
func (e *Engine) GetPortfolioID() uint {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.portfolioID
}

// AvailableCash 可自由支配现金 = 现金 - 待确认买入已占用资金
func (e *Engine) AvailableCash() float64 {
	e.mu.RLock()
	defer e.mu.RUnlock()
	a := e.cash - e.reservedBuy
	if a < 0 {
		a = 0
	}
	return a
}

// Buy 买入股票（模拟交易）
// decisionID 关联的DecisionObject决策ID（Trader只执行合法决策；手动交易可为空）
func (e *Engine) Buy(instrumentID, stockName, market string, quantity int, price float64, reason, decisionID string) (*TradeRecord, error) {
	// 紧急停止门控：紧急停止期间禁止一切买入（最终执行层防线，覆盖 CIO 建仓/自动股票池/place_trade/审批补确认所有入口）
	if e.emergencyStopped() {
		return nil, fmt.Errorf("紧急停止中，禁止买入: %s", instrumentID)
	}

	// 强制暂停建仓门控：暂停建仓期间禁止一切买入（最终执行层防线）。
	// 独立于紧急停止——仅拦截买入，卖出/离场/止损不受影响。
	if e.buyStopped() {
		return nil, fmt.Errorf("强制暂停建仓中，禁止买入: %s", instrumentID)
	}

	if quantity <= 0 || price <= 0 {
		return nil, fmt.Errorf("invalid quantity or price")
	}

	// A股最小单位检查
	if quantity%100 != 0 {
		return nil, fmt.Errorf("A股买入数量必须是100的整数倍")
	}

	// ST / *ST 风险警示股禁止买入（全局执行防线，覆盖 CIO 建仓/自动股票池/place_trade 所有入口）
	if data.IsSTName(stockName) {
		return nil, fmt.Errorf("ST/*ST 风险警示股禁止买入: %s", stockName)
	}

	// A股买入盘口/涨跌停拦截：跌停板禁止买入 + 涨停封死买不进。
	// （回测引擎已对跌停/涨停成交做约束，此处补上实盘端的同一约束。）
	if msg := e.buyBlockReason(instrumentID, stockName, price); msg != "" {
		return nil, fmt.Errorf("%s", msg)
	}

	// A股交易时段检查（后端强制校验，含节假日日历）
	if !util.IsTradingHour(time.Now()) {
		return nil, fmt.Errorf("当前为非交易时段，交易时段为 9:30-11:30 和 13:00-15:00（交易日）")
	}

	// 执行端硬风控（只拦不造，可叠加、不影响成交记账）：单票集中度 + 买入流动性拥挤度。
	// 这是“策略硬限制真正拦断下单”的执行喉舌——仅校验不过时拒绝，绝不伪造成交。
	if msg := e.buyRiskGate(instrumentID, quantity, price); msg != "" {
		return nil, fmt.Errorf("%s", msg)
	}

	grossAmount := float64(quantity) * price
	commission := grossAmount * 0.0003 // 万三佣金
	if commission < 5 {
		commission = 5
	}
	// A股印花税仅卖出时收取（0.05%），买入不收印花税
	fees := 0.0
	netAmount := grossAmount + commission + fees

	// 资金校验：可用资金 = 现金 - 已占用。若本笔买入已预占（reservedBuy 中含本单 gross），
	// 需回加本单预占额，避免「把本单自己的占用也算作不可用」导致重复扣减、误判资金不足。
	// 未预占（自动买入）时 selfReserved=0，保持原有保守校验，不触碰他人预留。
	selfReserved := grossAmount
	if e.reservedBuy < grossAmount {
		selfReserved = e.reservedBuy
	}
	avail := e.cash - e.reservedBuy + selfReserved
	if netAmount > avail {
		return nil, fmt.Errorf("资金不足: 需要%.2f, 可用%.2f(含待确认占用%.2f)", netAmount, avail, e.reservedBuy)
	}

	tradeID := fmt.Sprintf("T-%d-%s", time.Now().UnixNano(), instrumentID)

	trade := data.Trade{
		TradeID:      tradeID,
		OrderID:      fmt.Sprintf("O-%d", time.Now().UnixNano()),
		PortfolioID:  e.portfolioID,
		InstrumentID: instrumentID,
		Side:         "BUY",
		Quantity:     quantity,
		Price:        price,
		GrossAmount:  grossAmount,
		Commission:   commission,
		Fees:         fees,
		NetAmount:    netAmount,
		DecisionID:   decisionID,
		TradeDate:    time.Now(),
	}

	// 记录交易（即时同步落库，确保返回时成交记录已写入 sqlite，杜绝异步丢单/漂移）
	if err := e.db.GetDB().Create(&trade).Error; err != nil {
		log.Printf("[Portfolio] BUY trade 立即落库失败 %s: %v", instrumentID, err)
	}

	// 更新持仓
	e.mu.Lock()
	defer e.mu.Unlock()

	pos, exists := e.positions[instrumentID]
	if exists {
		// 加仓：更新平均成本
		totalQty := pos.Quantity + quantity
		pos.AvgCost = (pos.AvgCost*float64(pos.Quantity) + price*float64(quantity)) / float64(totalQty)
		pos.Quantity = totalQty
		pos.CurrentPrice = price
		pos.StockName = stockName
		pos.Market = market
		pos.LastUpdate = time.Now()
		// T+1：记录今日买入数量
		pos.TodayBoughtQuantity += quantity
	} else {
		// 新建持仓
		now := time.Now()
		e.positions[instrumentID] = &PositionState{
			InstrumentID:        instrumentID,
			StockName:           stockName,
			Market:              market,
			Quantity:            quantity,
			AvgCost:             price,
			CurrentPrice:        price,
			PrevClose:           price,
			OpenDate:            now,
			LastUpdate:          now,
			TodayBoughtQuantity: quantity,
		}
		pos = e.positions[instrumentID]
	}

	// 更新现金（成交后释放该笔买入占用，占用在创建待确认订单时已预占；净效果为可用现金仅扣手续费）
	e.cash -= netAmount
	e.reservedBuy -= grossAmount
	if e.reservedBuy < 0 {
		e.reservedBuy = 0
	}
	// 现金流水记账：每笔买入现金变动独立留痕（账本闭环，可独立对账）
	e.recordCashFlow(trade.OrderID, tradeID, instrumentID, "buy", "outflow", netAmount, -netAmount, reason)
	e.updatePositionMetrics(pos)
	e.updatePortfolio()

	// 保存持仓到数据库
	e.savePosition(instrumentID)

	log.Printf("[Portfolio] BUY %s %d股 @ ¥%.2f, 金额¥%.2f, 剩余现金¥%.2f",
		instrumentID, quantity, price, netAmount, e.cash)

	// 构建交易记录并输出JSON文件
	record := &TradeRecord{
		TradeID:      tradeID,
		Side:         "BUY",
		InstrumentID: instrumentID,
		StockName:    stockName,
		Quantity:     quantity,
		Price:        price,
		GrossAmount:  grossAmount,
		Commission:   commission,
		Fees:         fees,
		NetAmount:    netAmount,
		DecisionID:   decisionID,
		TradeDate:    time.Now(),
		Reason:       reason,
	}

	// 输出交易到JSON文件
	if err := e.writeTradeJSON(record); err != nil {
		log.Printf("[Portfolio] Failed to write trade JSON: %v", err)
	}

	return record, nil
}

// buyBlockReason 买入前盘口/涨跌停拦截。返回空串表示不拦截；非空返回拦截原因。
// 规则（基于实时快照真实行情，行情缺失时不拦截，避免因行情缺失误伤正常买入）：
//  1. 跌停板禁止买入：现价 ≤ 跌停价（昨收×(1-涨跌停幅度)）。跌停封板结局通常意味着
//     次日继续走弱，接飞刀风险大，所有买入路径统一在此拦截。
//  2. 涨停封死买不进：买一=涨停价且买一挂单量巨大（封单排队）→ 当天大概率成交不了，拦截。
func (e *Engine) buyBlockReason(instrumentID, stockName string, price float64) string {
	items, _ := data.FetchRealtimeStockSnapshots([]string{instrumentID})
	var snap data.StockSnapshot
	for _, s := range items {
		snap = s
		break
	}
	if snap.PrevClose <= 0 {
		return ""
	}
	limitPct := backtest.LimitPctForSymbol(instrumentID, stockName)
	limitDown := math.Round(snap.PrevClose*(1-limitPct)*100) / 100
	// 1) 跌停板禁止买入（触及跌停价含浮点容差）
	if price <= limitDown*(1+1e-9) {
		return fmt.Sprintf(
			"禁止在跌停板买入 %s(%s): 现价¥%.2f 已触及跌停价¥%.2f（昨收¥%.2f, 幅度%.1f%%）。跌停通常意味着封板下跌，接盘风险大，已拦截本次买入",
			stockName, instrumentID, price, limitDown, snap.PrevClose, limitPct*100)
	}
	// 2) 涨停封死买不进
	ob := orderbook.ClassifySnapshot(snap)
	if ob.State == orderbook.StateSealedLimitUp {
		return fmt.Sprintf("禁止买入 %s(%s): %s。封单排队当天买不进，已拦截本次买入",
			stockName, instrumentID, ob.Reason)
	}
	return ""
}

// buyRiskGate 执行端硬风控：单票集中度 + 买入流动性拥挤度。
// 只拦不造——校验不过时返回阻断原因，仅拒绝买单，绝不伪造成交或影响结算记账。
// 依赖已有实时快照，行情缺失时跳过拥挤度校验（不因缺数据误伤正常买入）。
func (e *Engine) buyRiskGate(instrumentID string, quantity int, price float64) string {
	const (
		maxSinglePositionPct = 0.30 // 单票占组合市值上限（与 policy 默认一致），超出拦截避免过度集中
		maxBuyCongestPct     = 0.08 // 单笔买入名义金额 ≤ 当日成交额的比例，防止在薄盘上大单打穿/买完卖不出
	)

	gross := float64(quantity) * price
	if gross <= 0 {
		return ""
	}

	// 单票集中度：已有持仓市值 + 本笔 → 不得超过组合市值上限
	e.mu.RLock()
	totalAssets := e.cash
	existingVal := 0.0
	for _, p := range e.positions {
		v := float64(p.Quantity) * p.CurrentPrice
		totalAssets += v
		if p.InstrumentID == instrumentID {
			existingVal = v
		}
	}
	e.mu.RUnlock()
	if totalAssets > 0 {
		afterPct := (existingVal + gross) / totalAssets
		if afterPct > maxSinglePositionPct {
			return fmt.Sprintf("单票集中度超限：买入后 %s 将占组合 %.1f%%（上限 %.0f%%），已拦截",
				instrumentID, afterPct*100, maxSinglePositionPct*100)
		}
	}

	// 买入流动性拥挤度：单笔名义金额过大且接近当日成交额 → 薄盘大单难成交/难离场
	items, _ := data.FetchRealtimeStockSnapshots([]string{instrumentID})
	var snap data.StockSnapshot
	for _, s := range items {
		snap = s
		break
	}
	if snap.Turnover > 0 && gross > snap.Turnover*maxBuyCongestPct {
		return fmt.Sprintf("买入流动性拥挤：本笔¥%.0f 超过 %s 当日成交额¥%.0f 的 %.0f%%，薄盘大单难成交/难离场，已拦截",
			gross, instrumentID, snap.Turnover, maxBuyCongestPct*100)
	}
	return ""
}

// sellBlockReason 卖出前盘口拦截：仅拦截「跌停封死卖不出」。
// 跌停封死（卖一=跌停价且卖一量巨大）时挂单卖出要排长队、当天无法成交，返回拦截保留持仓；
// 跌停打开（封单薄/巨量换手）放行，允许按跌停价立刻离场；行情缺失时不拦截（避免误伤）。
func (e *Engine) sellBlockReason(instrumentID, stockName string) string {
	items, _ := data.FetchRealtimeStockSnapshots([]string{instrumentID})
	var snap data.StockSnapshot
	for _, s := range items {
		snap = s
		break
	}
	if snap.PrevClose <= 0 {
		return ""
	}
	ob := orderbook.ClassifySnapshot(snap)
	if ob.State == orderbook.StateSealedLimitDown {
		return fmt.Sprintf("禁止在跌停封死时卖出 %s(%s): %s。挂单排队当天无法成交，保留持仓等待跌停打开",
			stockName, instrumentID, ob.Reason)
	}
	return ""
}

// Sell 卖出股票（模拟交易）
// decisionID 关联的DecisionObject决策ID（Trader只执行合法决策；手动交易可为空）
func (e *Engine) Sell(instrumentID string, quantity int, price float64, reason, decisionID string) (*TradeRecord, error) {
	// 紧急停止门控：紧急停止期间禁止一切卖出（最终执行层防线，覆盖 CIO 止损/自动股票池/place_trade/审批补确认所有入口）
	if e.emergencyStopped() {
		return nil, fmt.Errorf("紧急停止中，禁止卖出: %s", instrumentID)
	}

	if quantity <= 0 || price <= 0 {
		return nil, fmt.Errorf("invalid quantity or price")
	}

	e.mu.RLock()
	pos, exists := e.resolveSellPosition(instrumentID)
	e.mu.RUnlock()

	if !exists {
		return nil, fmt.Errorf("持仓中没有 %s", instrumentID)
	}

	// 使用解析出的规范持仓代码（可能为带前缀的 sh601700），替换原始入参，
	// 保证后续 delete/savePosition/trade 记录等操作都能一致命中同一持仓。
	instrumentID = pos.InstrumentID

	if pos.Quantity < quantity {
		return nil, fmt.Errorf("持仓数量不足: 需要%d, 持有%d", quantity, pos.Quantity)
	}

	// A股 T+1 交易规则检查（后端强制校验）：
	// 可卖数量 = 昨日持仓数量 = 当前持仓 - 今日买入数量
	// 当日买入的股票不能当日卖出
	// 按交易日判断是否已过 T+1 结算日：当前交易日晚于持仓最后更新的交易日则解锁
	now := time.Now()
	if util.IsTradingDay(now) {
		luDay := pos.LastUpdate
		if !util.IsTradingDay(luDay) {
			luDay = util.PrevTradingDay(luDay)
		}
		curDay := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
		if curDay.After(luDay) {
			pos.TodayBoughtQuantity = 0
		}
	}
	sellableQuantity := pos.Quantity - pos.TodayBoughtQuantity
	if sellableQuantity < 0 {
		sellableQuantity = 0
	}
	if quantity > sellableQuantity {
		return nil, fmt.Errorf("违反A股T+1交易规则：%s 可卖数量为 %d 股（昨日持仓），今日买入 %d 股不可卖出，请求卖出 %d 股",
			instrumentID, sellableQuantity, pos.TodayBoughtQuantity, quantity)
	}

	// A股卖出盘口拦截：跌停封死卖不出（卖一=跌停价且卖一量巨大 → 挂单排队当天无法成交）。
	// 跌停打开（封单薄/巨量换手）放行，允许按跌停价离场；行情缺失时不拦截，避免误伤正常减仓。
	if msg := e.sellBlockReason(instrumentID, pos.StockName); msg != "" {
		return nil, fmt.Errorf("%s", msg)
	}

	// A股交易时段检查（后端强制校验，含节假日日历）
	if !util.IsTradingHour(time.Now()) {
		return nil, fmt.Errorf("当前为非交易时段，交易时段为 9:30-11:30 和 13:00-15:00（交易日）")
	}

	grossAmount := float64(quantity) * price
	commission := grossAmount * 0.0003
	if commission < 5 {
		commission = 5
	}
	fees := grossAmount * 0.0005 // 卖出印花税0.05%
	netAmount := grossAmount - commission - fees
	realizedPnL := (price - pos.AvgCost) * float64(quantity)

	tradeID := fmt.Sprintf("T-%d-%s", time.Now().UnixNano(), instrumentID)

	trade := data.Trade{
		TradeID:      tradeID,
		OrderID:      fmt.Sprintf("O-%d", time.Now().UnixNano()),
		PortfolioID:  e.portfolioID,
		InstrumentID: instrumentID,
		Side:         "SELL",
		Quantity:     quantity,
		Price:        price,
		GrossAmount:  grossAmount,
		Commission:   commission,
		Fees:         fees,
		NetAmount:    netAmount,
		RealizedPnL:  realizedPnL,
		DecisionID:   decisionID,
		TradeDate:    time.Now(),
	}

	// 记录交易（即时同步落库，确保返回时成交记录已写入 sqlite，杜绝异步丢单/漂移）
	if err := e.db.GetDB().Create(&trade).Error; err != nil {
		log.Printf("[Portfolio] SELL trade 立即落库失败 %s: %v", instrumentID, err)
	}

	e.mu.Lock()
	defer e.mu.Unlock()

	pos.Quantity -= quantity
	if pos.Quantity == 0 {
		// 清仓 - 从内存中删除，并更新数据库状态
		delete(e.positions, instrumentID)
		// 更新数据库中的持仓状态为 closed
		if e.db != nil {
			e.db.GetDB().Model(&data.Position{}).
				Where("portfolio_id = ? AND instrument_id = ?", e.portfolioID, instrumentID).
				Updates(map[string]interface{}{
					"quantity":        0,
					"position_status": "closed",
					"updated_at":      time.Now(),
				})
		}
	} else {
		pos.CurrentPrice = price
		e.updatePositionMetrics(pos)
		// 更新数据库中的持仓
		e.savePosition(instrumentID)
	}

	e.cash += netAmount
	e.updatePortfolio()

	// 现金流水记账：每笔卖出现金变动独立留痕（账本闭环，可独立对账）
	e.recordCashFlow(trade.OrderID, tradeID, instrumentID, "sell", "inflow", netAmount, netAmount, reason)

	log.Printf("[Portfolio] SELL %s %d股 @ ¥%.2f, 金额¥%.2f, 剩余现金¥%.2f, 已实现盈亏¥%.2f",
		instrumentID, quantity, price, netAmount, e.cash, realizedPnL)

	// 构建交易记录并输出JSON文件
	record := &TradeRecord{
		TradeID:      tradeID,
		Side:         "SELL",
		InstrumentID: instrumentID,
		StockName:    pos.StockName,
		Quantity:     quantity,
		Price:        price,
		GrossAmount:  grossAmount,
		Commission:   commission,
		Fees:         fees,
		NetAmount:    netAmount,
		RealizedPnL:  realizedPnL,
		DecisionID:   decisionID,
		TradeDate:    time.Now(),
		Reason:       reason,
	}

	// 输出交易到JSON文件
	if err := e.writeTradeJSON(record); err != nil {
		log.Printf("[Portfolio] Failed to write trade JSON: %v", err)
	}

	return record, nil
}

// resolveSellPosition 解析卖出请求的持仓代码。
// 各调用方（风控/CIO/手工交易/Agent工具）传入的代码格式不统一，
// 可能为带前缀的 "sh601700"、大写的 "SH601700"，或纯数字的 "601700"。
// 这里先尝试精确匹配，未命中时再对持仓做前缀大小写与无前缀模糊匹配，
// 避免因代码格式不一致误报「持仓中没有 xxx」。必须在 e.mu 的 RLock/RUnlock 内调用。
func (e *Engine) resolveSellPosition(instrumentID string) (*PositionState, bool) {
	if pos, ok := e.positions[instrumentID]; ok {
		return pos, true
	}

	lowerInput := strings.ToLower(instrumentID)
	// 纯数字代码，如 "sh601700"/"601700" -> "601700"
	digitsOnly := positionDigitsOnly(instrumentID)

	for id, pos := range e.positions {
		if strings.ToLower(id) == lowerInput {
			return pos, true
		}
		// 纯数字代码与持仓代码的数字部分比对，如 "601700" 匹配 "sh601700"
		if digitsOnly != "" {
			if posAcc := positionDigitsOnly(id); posAcc == digitsOnly {
				return pos, true
			}
		}
	}
	return nil, false
}

// positionDigitsOnly 提取持仓代码中的纯数字部分（如 "sh601700" -> "601700"，"6" -> ""）
func positionDigitsOnly(id string) string {
	out := ""
	for i := 0; i < len(id); i++ {
		c := id[i]
		if c >= '0' && c <= '9' {
			out += string(c)
		}
	}
	return out
}

// UpdatePrices 用最新价格更新持仓（优化版：批量保存，减少数据库写入）
func (e *Engine) UpdatePrices(snapshots []data.StockSnapshot) {
	e.mu.Lock()
	defer e.mu.Unlock()

	log.Printf("[Portfolio] UpdatePrices: received %d snapshots, %d positions", len(snapshots), len(e.positions))

	// 构建多种格式的价格映射
	priceMap := make(map[string]float64)
	prevCloseMap := make(map[string]float64)
	for _, s := range snapshots {
		// 统一规范 key：小写市场前缀+数字（如 sh600519），兼容任意数据源产出格式
		normKey := data.MarketCode(s.Market + s.Code)
		priceMap[normKey] = s.CurrentPrice
		if s.PrevClose > 0 {
			prevCloseMap[normKey] = s.PrevClose
		}
		// 同时存储纯数字key
		priceMap[s.Code] = s.CurrentPrice
		if s.PrevClose > 0 {
			prevCloseMap[s.Code] = s.PrevClose
		}
	}

	// 反向映射：多种code格式 -> full symbol
	codeMap := make(map[string]string)
	for _, s := range snapshots {
		normKey := data.MarketCode(s.Market + s.Code)
		fullSymbol := strings.ToLower(normKey)
		// 多种格式映射
		codeMap[s.Code] = fullSymbol                        // "600519" -> "sh600519"
		codeMap[normKey] = fullSymbol                       // "sh600519" -> "sh600519"
		codeMap[strings.ToUpper(normKey)] = fullSymbol      // "SH600519" -> "sh600519"
		codeMap[data.PureCodeFromCode(s.Code)] = fullSymbol // 纯数字兜底
	}

	// 收集需要批量保存的持仓
	positionsToSave := make([]string, 0)
	updatedCount := 0

	for id, pos := range e.positions {
		// 尝试多种格式匹配
		matchedKey := ""
		price := 0.0
		found := false

		// 1. 直接用id匹配（转为小写）
		lowerID := strings.ToLower(id)
		if p, ok := priceMap[lowerID]; ok && p > 0 {
			price = p
			matchedKey = lowerID
			found = true
		}

		// 2. 尝试通过codeMap映射
		if !found {
			if fullSymbol, ok := codeMap[lowerID]; ok {
				if p, ok := priceMap[fullSymbol]; ok && p > 0 {
					price = p
					matchedKey = fullSymbol
					found = true
				}
			}
		}

		// 3. 如果id包含市场前缀，尝试拆分
		if !found && len(id) > 2 {
			prefix := strings.ToLower(id[:2])
			if prefix == "sh" || prefix == "sz" || prefix == "bj" {
				code := id[2:]
				if p, ok := priceMap[code]; ok && p > 0 {
					price = p
					matchedKey = code
					found = true
				}
			}
		}

		if found && price > 0 {
			pos.CurrentPrice = price
			// 更新PrevClose：仅当内存中没有PrevClose时才使用行情数据
			if pos.PrevClose <= 0 {
				if pc, ok := prevCloseMap[matchedKey]; ok && pc > 0 {
					pos.PrevClose = pc
					// 更新 PrevClose 缓存
					e.prevCloseCache[id] = pc
				}
			}
			e.updatePositionMetrics(pos)
			positionsToSave = append(positionsToSave, id)
			updatedCount++
			log.Printf("[Portfolio] Updated price for %s: %.2f (PrevClose: %.2f)", id, price, pos.PrevClose)
		} else {
			log.Printf("[Portfolio] Failed to match price for position: %s", id)
		}
	}

	// 批量保存持仓状态（每5秒或当队列长度超过10时保存一次）
	now := time.Now()
	shouldSave := len(e.pendingSaves) >= 10 || now.Sub(e.lastSaveTime) >= e.saveBatchInterval
	e.pendingSaves = append(e.pendingSaves, positionsToSave...)
	if shouldSave && len(e.pendingSaves) > 0 {
		e.batchSavePositionsLocked()
		e.lastSaveTime = now
	}

	log.Printf("[Portfolio] UpdatePrices complete: %d/%d positions updated, pending saves: %d", updatedCount, len(e.positions), len(e.pendingSaves))
	e.updatePortfolio()
}

// GetSnapshot 获取当前组合快照
func (e *Engine) GetSnapshot() *PortfolioSnapshot {
	// 以 sqlite 权威数据（position_snapshots 最近收盘快照 + 其后新成交）强制对账内存持仓，
	// 确保 CIO/操盘手决定加仓/减仓时读取的持仓始终以数据库为准，不残留内存/DB 漂移。
	e.reconcilePositionsFromTrades()

	// 先刷新价格，确保获取最新行情数据
	e.RefreshPrices()

	e.mu.RLock()

	positions := make([]*PositionState, 0, len(e.positions))
	totalMarketValue := 0.0
	totalCost := 0.0

	for _, pos := range e.positions {
		positions = append(positions, pos)
		totalMarketValue += pos.MarketValue
		totalCost += pos.AvgCost * float64(pos.Quantity)
	}

	snapshot := &PortfolioSnapshot{
		PortfolioID:      e.portfolioID,
		TotalCapital:     e.totalCapital,
		Cash:             e.cash,
		ReservedCash:     e.reservedBuy,
		AvailableCash:    e.cash - e.reservedBuy,
		TotalMarketValue: totalMarketValue,
		Positions:        positions,
		LastUpdated:      time.Now(),
	}
	e.mu.RUnlock()

	// 先加载 PrevClose，确保计算盈亏时有正确的基准价
	// loadPrevCloseFromDB 内部会获取必要的锁
	e.loadPrevCloseFromDB(snapshot)

	// 现在 PrevClose 已被正确加载，重新计算
	e.mu.RLock()
	defer e.mu.RUnlock()

	totalMarketValue = 0.0

	for _, pos := range snapshot.Positions {
		totalMarketValue += pos.MarketValue
	}

	totalAssets := e.cash + totalMarketValue
	// 累计盈亏 = 总资产 - 初始资金
	totalPnL := totalAssets - e.totalCapital
	totalReturn := 0.0
	if e.totalCapital > 0 {
		totalReturn = totalPnL / e.totalCapital * 100
	}

	snapshot.TotalMarketValue = totalMarketValue
	snapshot.TotalPnL = totalPnL
	snapshot.TotalReturn = totalReturn

	// 今日盈亏 = 今日总资产 - 昨日(基准)总资产（与每日结算 RecordDailySnapshot / GetPortfolioState 口径一致）
	// 首日无历史结算时，基准=期初资产，今日盈亏=累计盈亏。
	basisAssets, _ := e.getSettlementBasis()
	dailyPnL := totalAssets - basisAssets
	dailyReturn := 0.0
	if basisAssets > 0 {
		dailyReturn = dailyPnL / basisAssets * 100
	}
	snapshot.DailyPnL = dailyPnL
	snapshot.DailyReturn = dailyReturn

	// 统计有 PrevClose 的持仓数量
	prevCloseCount := 0
	for _, pos := range snapshot.Positions {
		if pos.PrevClose > 0 {
			prevCloseCount++
		}
	}
	log.Printf("[Portfolio] GetSnapshot: totalAssets=%.2f, dailyPnL=%.2f, dailyReturn=%.2f%%, positions=%d, prevClosePositions=%d",
		totalAssets, dailyPnL, snapshot.DailyReturn, len(snapshot.Positions), prevCloseCount)

	return snapshot
}

// GetTradeHistory 获取交易历史
func (e *Engine) GetTradeHistory(days int) ([]TradeRecord, error) {
	if e.db == nil {
		return nil, fmt.Errorf("database not initialized")
	}

	var trades []data.Trade
	since := time.Now().AddDate(0, 0, -days)
	e.db.GetDB().
		Where("portfolio_id = ? AND trade_date >= ?", e.portfolioID, since).
		Order("trade_date DESC").
		Find(&trades)

	result := make([]TradeRecord, len(trades))
	for i, t := range trades {
		result[i] = TradeRecord{
			TradeID:      t.TradeID,
			Side:         t.Side,
			InstrumentID: t.InstrumentID,
			Quantity:     t.Quantity,
			Price:        t.Price,
			GrossAmount:  t.GrossAmount,
			Commission:   t.Commission,
			Fees:         t.Fees,
			NetAmount:    t.NetAmount,
			RealizedPnL:  t.RealizedPnL,
			TradeDate:    t.TradeDate,
		}
	}

	return result, nil
}

// GetTodayTrades 获取今日交易记录
func (e *Engine) GetTodayTrades() ([]TradeRecord, error) {
	if e.db == nil {
		return nil, fmt.Errorf("database not initialized")
	}

	todayStart := time.Now().Truncate(24 * time.Hour)
	var trades []data.Trade
	e.db.GetDB().
		Where("portfolio_id = ? AND trade_date >= ?", e.portfolioID, todayStart).
		Order("trade_date DESC").
		Find(&trades)

	result := make([]TradeRecord, len(trades))
	for i, t := range trades {
		result[i] = TradeRecord{
			TradeID:      t.TradeID,
			Side:         t.Side,
			InstrumentID: t.InstrumentID,
			Quantity:     t.Quantity,
			Price:        t.Price,
			GrossAmount:  t.GrossAmount,
			Commission:   t.Commission,
			Fees:         t.Fees,
			NetAmount:    t.NetAmount,
			RealizedPnL:  t.RealizedPnL,
			TradeDate:    t.TradeDate,
		}
	}

	return result, nil
}

// StartContinuousTrading 启动持续交易监控
func (e *Engine) StartContinuousTrading(ctx context.Context, interval time.Duration) {
	e.mu.Lock()
	if e.running {
		e.mu.Unlock()
		return
	}
	e.running = true
	e.stopCh = make(chan struct{})
	e.mu.Unlock()

	log.Printf("[Portfolio] Continuous trading started, interval=%v", interval)

	util.SafeGo("Portfolio.ContinuousTrading", func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		for {
			select {
			case <-e.stopCh:
				log.Printf("[Portfolio] Continuous trading stopped")
				return
			case <-ticker.C:
				e.tick()
			case <-ctx.Done():
				log.Printf("[Portfolio] Continuous trading context cancelled")
				return
			}
		}
	})
}

// StopContinuousTrading 停止持续交易
func (e *Engine) StopContinuousTrading() {
	e.mu.Lock()
	defer e.mu.Unlock()

	if e.running {
		e.running = false
		close(e.stopCh)
		log.Printf("[Portfolio] Continuous trading stop requested")
	}
}

// IsRunning 检查是否在运行
func (e *Engine) IsRunning() bool {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.running
}

// tick 每次定时执行：刷新价格、检查风险
func (e *Engine) tick() {
	if !isTradingTime() {
		return
	}

	// 拉取实时行情（复用 RefreshPrices 的短 TTL 缓存，只返回真实数据）
	snapshots := e.RefreshPrices()
	if len(snapshots) > 0 {
		snapshot := e.GetSnapshot()
		log.Printf("[Portfolio] Tick: 资产¥%.2f, 现金¥%.2f, 持仓市值¥%.2f, 盈亏¥%.2f (%.2f%%), 数据源=realtime",
			e.cash+snapshot.TotalMarketValue,
			e.cash,
			snapshot.TotalMarketValue,
			snapshot.TotalPnL,
			snapshot.TotalReturn)
	}
}

// updatePositionMetrics 更新单个持仓的指标
func (e *Engine) updatePositionMetrics(pos *PositionState) {
	pos.MarketValue = pos.CurrentPrice * float64(pos.Quantity)
	if pos.AvgCost > 0 {
		pos.UnrealizedPnL = (pos.CurrentPrice - pos.AvgCost) * float64(pos.Quantity)
		pos.UnrealizedReturn = (pos.CurrentPrice - pos.AvgCost) / pos.AvgCost * 100
	}
}

// updatePortfolio 更新组合总资产和权重
func (e *Engine) updatePortfolio() {
	totalMV := 0.0
	for _, pos := range e.positions {
		totalMV += pos.MarketValue
	}
	totalAssets := e.cash + totalMV
	cash := e.cash
	// 累计盈亏基准：恒定为组合 InitialCapital（与 GetPortfolioState 口径一致），
	// 禁止使用会随总资产漂移的 e.totalCapital/current_capital，否则累计盈亏会失真。
	initialCapital := e.getInitialCapital()
	totalPnl := totalAssets - initialCapital
	totalReturn := 0.0
	if initialCapital > 0 {
		totalReturn = totalPnl / initialCapital
	}

	for _, pos := range e.positions {
		if totalAssets > 0 {
			pos.Weight = pos.MarketValue / totalAssets * 100
		}
	}

	// 更新数据库中的组合 - 使用SafeGoWithRetry并预捕获变量
	if e.db != nil {
		util.SafeGoWithRetry("Portfolio.updatePortfolio", 3, func() error {
			return e.db.GetDB().Model(&data.Portfolio{}).
				Where("id = ?", e.portfolioID).
				Updates(map[string]interface{}{
					"current_capital": totalAssets,
					"cash":            cash,
					"total_pnl":       totalPnl,
					"total_return":    totalReturn,
					"updated_at":      time.Now(),
				}).Error
		})
	}
}

// savePosition 保存单个持仓到数据库
func (e *Engine) savePosition(instrumentID string) {
	pos, exists := e.positions[instrumentID]
	if !exists {
		return
	}

	var dbPos data.Position
	result := e.db.GetDB().Where("portfolio_id = ? AND instrument_id = ?", e.portfolioID, instrumentID).First(&dbPos)

	if result.Error != nil {
		// 新建
		dbPos = data.Position{
			PortfolioID:      e.portfolioID,
			InstrumentID:     instrumentID,
			Quantity:         pos.Quantity,
			AvgCost:          pos.AvgCost,
			CurrentPrice:     pos.CurrentPrice,
			MarketValue:      pos.MarketValue,
			UnrealizedPnL:    pos.UnrealizedPnL,
			UnrealizedReturn: pos.UnrealizedReturn,
			Weight:           pos.Weight / 100,
			PositionStatus:   "open",
		}
		e.db.GetDB().Create(&dbPos)
	} else {
		// 更新（清仓后再买入时需把 position_status 重置为 open，否则 initPortfolio 按 open 过滤会丢失该持仓）
		e.db.GetDB().Model(&dbPos).Updates(map[string]interface{}{
			"quantity":          pos.Quantity,
			"avg_cost":          pos.AvgCost,
			"current_price":     pos.CurrentPrice,
			"market_value":      pos.MarketValue,
			"unrealized_pnl":    pos.UnrealizedPnL,
			"unrealized_return": pos.UnrealizedReturn,
			"weight":            pos.Weight / 100,
			"position_status":   "open",
			"updated_at":        time.Now(),
		})
	}
}

// batchSavePositionsLocked 批量保存持仓到数据库（调用方必须持有 e.mu 锁）
func (e *Engine) batchSavePositionsLocked() {
	if e.db == nil || len(e.pendingSaves) == 0 {
		e.pendingSaves = nil
		return
	}

	// 去重
	uniqueIDs := make(map[string]bool)
	for _, id := range e.pendingSaves {
		uniqueIDs[id] = true
	}
	e.pendingSaves = nil

	now := time.Now()
	savedCount := 0
	for instrumentID := range uniqueIDs {
		pos, exists := e.positions[instrumentID]
		if !exists {
			continue
		}

		var dbPos data.Position
		result := e.db.GetDB().Where("portfolio_id = ? AND instrument_id = ?", e.portfolioID, instrumentID).First(&dbPos)

		if result.Error != nil {
			// 新建
			dbPos = data.Position{
				PortfolioID:      e.portfolioID,
				InstrumentID:     instrumentID,
				Quantity:         pos.Quantity,
				AvgCost:          pos.AvgCost,
				CurrentPrice:     pos.CurrentPrice,
				MarketValue:      pos.MarketValue,
				UnrealizedPnL:    pos.UnrealizedPnL,
				UnrealizedReturn: pos.UnrealizedReturn,
				Weight:           pos.Weight / 100,
				PositionStatus:   "open",
			}
			if err := e.db.GetDB().Create(&dbPos).Error; err == nil {
				savedCount++
			}
		} else {
			// 批量更新（清仓后再买入时须把 position_status 重置为 open，
			// 否则 initPortfolio 按 open 过滤会丢失该持仓 → 与 savePosition 保持一致）
			result := e.db.GetDB().Model(&dbPos).Updates(map[string]interface{}{
				"quantity":          pos.Quantity,
				"avg_cost":          pos.AvgCost,
				"current_price":     pos.CurrentPrice,
				"market_value":      pos.MarketValue,
				"unrealized_pnl":    pos.UnrealizedPnL,
				"unrealized_return": pos.UnrealizedReturn,
				"weight":            pos.Weight / 100,
				"position_status":   "open",
				"updated_at":        now,
			})
			if result.Error == nil {
				savedCount++
			}
		}
	}

	log.Printf("[Portfolio] Batch save completed: %d positions saved at %s", savedCount, now.Format("15:04:05"))
}

// resolveDisplayName 返回持仓展示名称：StockName 为空或等于代码时，用字典兜底补齐。
// 实时行情 Name 可能为空串，或补确认等路径留下空名称持仓，避免前端持仓明细只显示代码。
func (e *Engine) resolveDisplayName(pos *PositionState) string {
	if pos.StockName != "" && pos.StockName != pos.InstrumentID {
		return pos.StockName
	}
	dl := data.GetDictLoader()
	pure := pos.InstrumentID
	for _, key := range []string{pure, "sh" + pure, "sz" + pure, "bj" + pure, "SH" + pure, "SZ" + pure, "BJ" + pure} {
		if n := dl.GetStockName(key); n != "" && n != pure {
			return n
		}
	}
	return pos.StockName
}

// GetPositionsJSON 获取持仓JSON（用于API）
func (e *Engine) GetPositionsJSON() ([]map[string]interface{}, error) {
	e.mu.RLock()
	defer e.mu.RUnlock()

	result := make([]map[string]interface{}, 0, len(e.positions))
	for _, pos := range e.positions {
		result = append(result, map[string]interface{}{
			"instrument_id":     pos.InstrumentID,
			"stock_name":        e.resolveDisplayName(pos),
			"market":            pos.Market,
			"quantity":          pos.Quantity,
			"avg_cost":          pos.AvgCost,
			"current_price":     pos.CurrentPrice,
			"market_value":      pos.MarketValue,
			"unrealized_pnl":    pos.UnrealizedPnL,
			"unrealized_return": pos.UnrealizedReturn,
			"weight":            pos.Weight,
		})
	}

	// 按权重排序
	sort.Slice(result, func(i, j int) bool {
		return result[i]["weight"].(float64) > result[j]["weight"].(float64)
	})

	return result, nil
}

// isTradingTime 判断是否为A股交易时间（统一使用交易日历 + 交易时段）
func isTradingTime() bool {
	return util.IsTradingHour(time.Now())
}

// ExportJSON 导出组合状态为JSON
func (e *Engine) ExportJSON() (string, error) {
	snapshot := e.GetSnapshot()
	positionsJSON, _ := json.Marshal(snapshot)
	return string(positionsJSON), nil
}

// GetCash 获取当前现金
func (e *Engine) GetCash() float64 {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.cash
}

// Reset 重置组合（仅限测试/重置功能）
func (e *Engine) Reset(newCapital float64) {
	e.mu.Lock()
	defer e.mu.Unlock()

	e.positions = make(map[string]*PositionState)
	e.cash = newCapital
	e.totalCapital = newCapital
	e.updatePortfolio()

	// 同步更新数据库中的期初资金 InitialCapital，使累计盈亏口径一致。
	// 若不更新，getInitialCapital() 会优先读到旧的 InitialCapital（如 10 万），
	// 导致 Reset(0) 后累计盈亏 = 总资产(0) - 期初(10万) = -10万 的失真显示。
	// 同时清空该组合的每日结算记录，使今日盈亏基准回退到新的期初资金，
	// 避免旧结算(如昨日总资产 10 万)导致今日盈亏 = 0 - 10万 = -10万 的失真显示。
	if e.db != nil {
		gdb := e.db.GetDB()
		gdb.Model(&data.Portfolio{}).
			Where("id = ?", e.portfolioID).
			Updates(map[string]interface{}{
				"initial_capital": newCapital,
			})
		gdb.Where("portfolio_id = ?", e.portfolioID).
			Delete(&data.PortfolioDailyStat{})
	}

	log.Printf("[Portfolio] Reset: cash=%.2f, initial_capital=%.2f, daily_stats cleared", e.cash, newCapital)
}

// LedgerSummary 财务流水账对账汇总（四账体系，防止盈亏错乱）：
//
//	期初账 opening  : portfolios.initial_capital（恒定不变）
//	临时账 temp     : orders 表 pending 待确认买入 → reservedBuy 占用（挂单未成交）
//	明细账 detail   : trades 表 成交流水（笔数 / Σ买入净额 / Σ卖出净额）
//	总账   general  : portfolio_daily_stats 最近结算（总资产 / 累计盈亏 / 今日盈亏）
//
// 并校验：现金推导(=期初+Σ卖净−Σ买净) 与 DB 现金一致、总资产=现金+持仓市值。
// 返回 reconciled=true 表示四账对平；issues 列出差异（前端/日志可直接展示）。
func (e *Engine) LedgerSummary() map[string]interface{} {
	result := map[string]interface{}{
		"reconciled": true,
		"issues":     []string{},
	}
	if e.db == nil {
		return result
	}
	e.reconcilePositionsFromTrades() // 内部已含持仓+现金对账，保证本次汇总基于权威数据

	opening := e.getInitialCapital()
	e.mu.RLock()
	cash := e.cash
	reserved := e.reservedBuy
	positions := make([]*PositionState, 0, len(e.positions))
	for _, pos := range e.positions {
		positions = append(positions, pos)
	}
	e.mu.RUnlock()

	// 明细账：成交流水汇总
	var buyNet, sellNet float64
	var buyCnt, sellCnt, tradeCnt int
	var trades []data.Trade
	if err := e.db.GetDB().Where("portfolio_id = ?", e.portfolioID).Find(&trades).Error; err == nil {
		for _, t := range trades {
			switch t.Side {
			case "BUY":
				buyNet += t.NetAmount
				buyCnt++
			case "SELL":
				sellNet += t.NetAmount
				sellCnt++
			}
		}
		tradeCnt = len(trades)
	}

	// 临时账：待确认挂单
	var pendingCount int64
	e.db.GetDB().Model(&data.Order{}).Where("portfolio_id = ? AND status = ?", e.portfolioID, "pending").Count(&pendingCount)

	// 总账：最近结算
	var generalDate string
	var generalTotalAssets, generalTotalPnL, generalDailyPnL float64
	var latest data.PortfolioDailyStat
	if err := e.db.GetDB().Where("portfolio_id = ?", e.portfolioID).Order("stat_date DESC").First(&latest).Error; err == nil {
		generalDate = latest.StatDate
		generalTotalAssets = latest.TotalAssets
		generalTotalPnL = latest.TotalPnL
		generalDailyPnL = latest.DailyPnL
	}

	// 现金推导 = 期初 + Σ卖净 − Σ买净
	cashDerived := opening + sellNet - buyNet

	// 当前总资产 = 现金 + 持仓市值
	totalMV := 0.0
	for _, pos := range positions {
		totalMV += pos.MarketValue
	}
	totalAssets := cash + totalMV

	issues := []string{}
	if abs1(cash-cashDerived) > 0.01 {
		issues = append(issues, fmt.Sprintf("现金不一致：内存/DB=%.2f，明细账推导=%.2f", cash, cashDerived))
	}
	if generalDate != "" && abs1(generalTotalPnL-(generalTotalAssets-opening)) > 1.0 {
		issues = append(issues, fmt.Sprintf("总账累计盈亏(%.2f)≠总资产-期初(%.2f)", generalTotalPnL, generalTotalAssets-opening))
	}

	result["opening_capital"] = opening
	result["temp"] = map[string]interface{}{
		"pending_orders": pendingCount,
		"reserved_buy":   reserved,
	}
	result["detail"] = map[string]interface{}{
		"trade_count": tradeCnt,
		"buy_count":   buyCnt,
		"buy_net":     buyNet,
		"sell_count":  sellCnt,
		"sell_net":    sellNet,
	}
	result["general"] = map[string]interface{}{
		"date":         generalDate,
		"total_assets": generalTotalAssets,
		"total_pnl":    generalTotalPnL,
		"daily_pnl":    generalDailyPnL,
	}
	result["cash"] = map[string]interface{}{
		"derived":   cashDerived,
		"on_record": cash,
	}
	result["total_assets"] = totalAssets
	result["reconciled"] = len(issues) == 0
	result["issues"] = issues
	return result
}

// GetPortfolioState 获取组合状态（JSON友好格式）
// GetPortfolioState 获取组合状态（JSON友好格式）
// 定价规则：交易时段持仓价值取实时行情；非交易时段取数据库最近结算收盘价（不联网）
// 盈亏口径（数据驱动，数据库每日结算）：
//
//	今日盈亏 = 今日总资产 - 昨日(基准)总资产
//	累计盈亏 = 今日总资产 - 期初资产
//	(总资产 = 现金 + 持仓价值)
//
// 本函数仅加载持仓行情/昨收/结算数据，独立于因子回测与选股引擎，不复用其链路。
func (e *Engine) GetPortfolioState() map[string]interface{} {
	// 每次读取前以 sqlite 权威数据（position_snapshots 最近收盘快照 + 其后新成交）强制对账内存持仓与现金，
	// 确保前端展示始终与数据库真实持仓/现金一致（含 CIO/操盘手/外部对其余路径写库后的实时同步）。
	e.reconcilePositionsFromTrades()
	e.syncPrices()

	e.mu.RLock()
	cash := e.cash
	positions := make([]*PositionState, 0, len(e.positions))
	for _, pos := range e.positions {
		positions = append(positions, pos)
	}
	e.mu.RUnlock()

	// 汇总持仓市值 = 现金 + 持仓价值
	totalMarketValue := 0.0
	for _, pos := range positions {
		totalMarketValue += pos.MarketValue
	}
	totalAssets := cash + totalMarketValue

	// 持仓权重
	for _, pos := range positions {
		if totalAssets > 0 {
			pos.Weight = pos.MarketValue / totalAssets * 100
		} else {
			pos.Weight = 0
		}
	}

	// 期初资产（组合 InitialCapital，恒定不变）与昨日(基准)总资产
	initialCapital := e.getInitialCapital()
	basisAssets, basisDate := e.getSettlementBasis()

	// 累计盈亏 = 今日总资产 - 期初资产
	totalPnL := totalAssets - initialCapital
	totalReturn := 0.0
	if initialCapital > 0 {
		totalReturn = totalPnL / initialCapital * 100
	}
	// 今日盈亏 = 今日总资产 - 昨日(基准)总资产（与每日结算 RecordDailySnapshot 口径一致）
	// 首日无历史结算时，基准=期初资产，故今日盈亏=累计盈亏；严禁用逐股(现价-昨收)×量 求和，
	// 那会忽略当日新开仓/手续费影响导致与数据库结算值(portfolio_daily_stats.daily_pnl)不一致。
	dailyPnL := totalAssets - basisAssets
	dailyReturn := 0.0
	if basisAssets > 0 {
		dailyReturn = dailyPnL / basisAssets * 100
	}

	positionsJSON := make([]map[string]interface{}, 0, len(positions))
	for _, pos := range positions {
		// 单股今日盈亏明细：
		// - 今日新开仓（OpenDate 为今日）→ (现价 - 成本价) × 量，从买入时点起算
		// - 昨日已有持仓 → (现价 - 昨收价) × 量，反映当日涨跌
		posDailyPnL := 0.0
		posDailyReturn := 0.0
		if pos.Quantity > 0 {
			refPrice := pos.PrevClose
			if openedToday(pos.OpenDate) {
				refPrice = pos.AvgCost
			}
			if refPrice > 0 {
				posDailyPnL = (pos.CurrentPrice - refPrice) * float64(pos.Quantity)
				costBasis := refPrice * float64(pos.Quantity)
				if costBasis > 0 {
					posDailyReturn = posDailyPnL / costBasis * 100
				}
			}
		}
		positionsJSON = append(positionsJSON, map[string]interface{}{
			"code":             pos.InstrumentID,
			"stockName":        e.resolveDisplayName(pos),
			"quantity":         pos.Quantity,
			"currentPrice":     pos.CurrentPrice,
			"prevClose":        pos.PrevClose,
			"avgCost":          pos.AvgCost,
			"marketValue":      pos.MarketValue,
			"unrealizedPnL":    pos.UnrealizedPnL,
			"unrealizedReturn": pos.UnrealizedReturn,
			"dailyPnL":         posDailyPnL,
			"dailyReturn":      posDailyReturn,
			"weight":           pos.Weight,
			"lastUpdate":       pos.LastUpdate.Format(time.RFC3339),
		})
	}

	// 获取交易记录
	tradeRecords, _ := e.GetTradeHistory(30)
	trades := make([]map[string]interface{}, 0, len(tradeRecords))
	for _, t := range tradeRecords {
		trades = append(trades, map[string]interface{}{
			"tradeID":      t.TradeID,
			"side":         t.Side,
			"instrumentID": t.InstrumentID,
			"quantity":     t.Quantity,
			"price":        t.Price,
			"grossAmount":  t.GrossAmount,
			"commission":   t.Commission,
			"fees":         t.Fees,
			"netAmount":    t.NetAmount,
			"realizedPnL":  t.RealizedPnL,
			"tradeDate":    t.TradeDate.Format(time.RFC3339),
		})
	}

	return map[string]interface{}{
		"portfolioID":      e.portfolioID,
		"totalCapital":     initialCapital, // 期初本金
		"totalAssets":      totalAssets,    // 总资产 = 现金 + 持仓价值
		"cash":             cash,
		"reservedCash":     e.reservedBuy,        // 待确认买入已占用资金
		"availableCash":    cash - e.reservedBuy, // 可自由支配现金
		"totalMarketValue": totalMarketValue,     // 持仓价值
		"totalPnL":         totalPnL,             // 累计盈亏
		"totalReturn":      totalReturn,          // 累计收益率
		"dailyPnL":         dailyPnL,             // 今日盈亏 = 今日总资产 - 昨日总资产
		"dailyReturn":      dailyReturn,          // 今日收益率
		"basisAssets":      basisAssets,          // 昨日(基准)总资产
		"basisDate":        basisDate,            // 基准日期
		"positionsCount":   len(positions),
		"positions":        positionsJSON,
		"recentTrades":     trades[:min(len(trades), 20)],
		"ledger":           e.LedgerSummary(), // 财务流水账四账对账汇总（期初/临时账/明细账/总账）
		"lastUpdated":      time.Now().Format(time.RFC3339),
	}
}

// syncPrices 同步持仓定价：交易时段用实时行情；非交易时段用最近结算收盘价（不联网）
func (e *Engine) syncPrices() {
	// 数据源规则：仅活跃交易时段（9:30-11:30 / 13:00-15:00）才用实时行情刷新内存价；
	// 其余任何时段（盘前/午休/盘后/复盘/周末/节假日）一律采用数据库最近结算收盘快照计价，
	// 严禁使用内存实时值，确保非交易时段前端展示与数据库结算口径一致、不漂移。
	if util.IsTradingHour(time.Now()) {
		e.RefreshPrices()
		// 交易时段加载昨收价（含缓存），供单股今日盈亏明细使用
		e.mu.RLock()
		positions := make([]*PositionState, 0, len(e.positions))
		for _, pos := range e.positions {
			positions = append(positions, pos)
		}
		e.mu.RUnlock()
		e.loadPrevCloseFromDB(&PortfolioSnapshot{Positions: positions})
	} else {
		// 非交易时段：使用最近结算收盘价计价（数据库值，严禁内存实时值）
		e.loadSettlementPrices()
	}
}

// loadSettlementPrices 从数据库加载最近结算收盘价作为当前持仓计价（非交易时段）
func (e *Engine) loadSettlementPrices() {
	if e.db == nil {
		return
	}
	e.mu.RLock()
	hasPositions := len(e.positions) > 0
	instruments := make([]string, 0, len(e.positions))
	for id := range e.positions {
		instruments = append(instruments, id)
	}
	e.mu.RUnlock()
	if !hasPositions {
		return
	}

	// 最近结算日：存在持仓快照的最近日期
	var latest data.PositionSnapshot
	err := e.db.GetDB().
		Where("portfolio_id = ? AND instrument_id IN ?", e.portfolioID, instruments).
		Order("snapshot_date DESC").First(&latest).Error
	if err != nil {
		return
	}
	settleDate := latest.SnapshotDate
	if settleDate == "" {
		return
	}

	var snaps []data.PositionSnapshot
	e.db.GetDB().
		Where("portfolio_id = ? AND snapshot_date = ?", e.portfolioID, settleDate).
		Find(&snaps)
	snapByID := make(map[string]data.PositionSnapshot, len(snaps))
	for _, s := range snaps {
		snapByID[s.InstrumentID] = s
	}

	e.mu.Lock()
	for _, pos := range e.positions {
		s, ok := snapByID[pos.InstrumentID]
		if !ok || s.ClosePrice <= 0 {
			continue
		}
		pos.CurrentPrice = s.ClosePrice
		pos.MarketValue = s.ClosePrice * float64(pos.Quantity)
		pos.PrevClose = s.PrevClose
		pos.UnrealizedPnL = (s.ClosePrice - pos.AvgCost) * float64(pos.Quantity)
		if pos.AvgCost > 0 {
			pos.UnrealizedReturn = (s.ClosePrice - pos.AvgCost) / pos.AvgCost * 100
		} else {
			pos.UnrealizedReturn = 0
		}
		pos.LastUpdate = time.Now()
	}
	e.mu.Unlock()

	log.Printf("[Portfolio] Loaded settlement prices for date=%s, positions=%d", settleDate, len(snapByID))
}

// getInitialCapital 获取期初资产（组合 InitialCapital，恒定不变）
// 不使用 CurrentCapital（会随总资产变化），确保累计盈亏口径稳定
func (e *Engine) getInitialCapital() float64 {
	if e.db == nil {
		return e.totalCapital
	}
	var p data.Portfolio
	if err := e.db.GetDB().Where("id = ?", e.portfolioID).First(&p).Error; err == nil && p.InitialCapital > 0 {
		return p.InitialCapital
	}
	return e.totalCapital
}

// getSettlementBasis 获取昨日(基准)总资产及其结算日期
// 今日盈亏 = 今日总资产 - 基准总资产
// 取数据库最接近今天的已结算 PortfolioDailyStat（stat_date < 今日）
// 无历史记录时以期初资产作为基准
func (e *Engine) getSettlementBasis() (float64, string) {
	if e.db == nil {
		return e.getInitialCapital(), ""
	}
	today := time.Now().Format("2006-01-02")
	var s data.PortfolioDailyStat
	err := e.db.GetDB().
		Where("portfolio_id = ? AND stat_date < ?", e.portfolioID, today).
		Order("stat_date DESC").First(&s).Error
	if err != nil {
		return e.getInitialCapital(), ""
	}
	return s.TotalAssets, s.StatDate
}

// ValidateSettlement 校验组合每日结算数据的正确性（启动时调用）。
// 返回发现的问题列表；返回空表示结算数据一致、未发现异常。
// 校验项：
//  1. 是否存在每日结算记录（PortfolioDailyStat）；缺失意味着 今日盈亏 会退化为 累计盈亏。
//  2. 最近结算日期是否最近（交易日应当有当日/昨日结算）。
//  3. 每条结算记录的 TotalAssets 是否等于 Cash + MarketValue。
//  4. 每条结算记录的 TotalPnL(累计) 是否等于 TotalAssets - InitialCapital。
//  5. 今日结算是否已存在且与当前组合状态一致。
func (e *Engine) ValidateSettlement() []string {
	if e.db == nil {
		return []string{"数据库未初始化，无法校验组合结算数据"}
	}

	initialCapital := e.getInitialCapital()
	var issues []string

	var stats []data.PortfolioDailyStat
	if err := e.db.GetDB().
		Where("portfolio_id = ?", e.portfolioID).
		Order("stat_date ASC").
		Find(&stats).Error; err != nil {
		return []string{fmt.Sprintf("读取每日结算记录失败: %v", err)}
	}

	today := time.Now().Format("2006-01-02")

	// 1) 有没有任何结算记录
	if len(stats) == 0 {
		issues = append(issues, "未找到任何每日结算记录（PortfolioDailyStat 为空）：今日盈亏将退化为累计盈亏，请确保每日盘后执行 daily_settlement 工具完成结算")
	} else {
		// 2) 最近结算日期是否距今过远（超过3个自然日说明结算中断）
		latest := stats[len(stats)-1]
		latestDate, perr := time.Parse("2006-01-02", latest.StatDate)
		if perr == nil {
			daysGap := int(time.Now().Sub(latestDate).Hours() / 24)
			if daysGap > 3 {
				issues = append(issues, fmt.Sprintf("最近结算日期 %s 距今已 %d 天，疑似中途未结算，可能影响今日盈亏基准", latest.StatDate, daysGap))
			}
		}
	}

	// 3) 与 4) 逐条结算记录一致性
	for _, s := range stats {
		sum := s.Cash + s.MarketValue
		if abs1(s.TotalAssets-sum) > 1.0 {
			issues = append(issues, fmt.Sprintf("结算 %s 总资产(%.2f) 与 现金+持仓(%.2f) 不一致", s.StatDate, s.TotalAssets, sum))
		}
		expectedPnl := s.TotalAssets - initialCapital
		if abs1(s.TotalPnL-expectedPnl) > 1.0 {
			issues = append(issues, fmt.Sprintf("结算 %s 累计盈亏(%.2f) 与 总资产-期初(%.2f) 不一致", s.StatDate, s.TotalPnL, expectedPnl))
		}
	}

	// 5) 今日是否已有结算、且与当前组合一致
	var todayStat data.PortfolioDailyStat
	terr := e.db.GetDB().
		Where("portfolio_id = ? AND stat_date = ?", e.portfolioID, today).
		First(&todayStat).Error
	if terr == nil {
		sum := todayStat.Cash + todayStat.MarketValue
		if abs1(todayStat.TotalAssets-sum) > 1.0 {
			issues = append(issues, fmt.Sprintf("今日结算 %s 总资产(%.2f) 与 现金+持仓(%.2f) 不一致", today, todayStat.TotalAssets, sum))
		}
	} else {
		// 交易时段（盘后结算前）今天还没有结算记录是正常的，仅提示
		issues = append(issues, fmt.Sprintf("今日(%s)尚无结算记录：若已收盘请确保执行每日结算", today))
	}

	return issues
}

func abs1(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}

// openedToday 判断开仓时间是否在今日（用于逐股今日盈亏明细基准：今日新开仓用成本价，否则用昨收价）
func openedToday(t time.Time) bool {
	if t.IsZero() {
		return false
	}
	now := time.Now()
	return t.Year() == now.Year() && t.Month() == now.Month() && t.Day() == now.Day()
}

// WarmUp 预热组合数据（启动页调用）
// 交易日（含午休/盘后/复盘等非活跃时段）：刷新持仓实时行情缓存；非交易日：加载最近结算收盘价与基准数据。
// 与 syncPrices 保持一致：交易日始终用实时价计价，避免午间/盘后启动时把正确的实时价覆盖成昨日收盘价。
// 仅处理持仓行情/昨收/结算，独立于因子回测与选股引擎。
// 网络或数据异常时返回错误（不降级）。
func (e *Engine) WarmUp() error {
	if util.IsTradingDay(time.Now()) {
		snaps := e.RefreshPrices()
		e.mu.RLock()
		hasPositions := len(e.positions) > 0
		e.mu.RUnlock()
		if hasPositions && len(snaps) == 0 {
			return fmt.Errorf("交易日预热行情失败：无法获取持仓实时行情，请检查网络连接或数据源")
		}
	} else {
		e.loadSettlementPrices()
	}
	// 预热基准与期初（读取验证）
	_, _ = e.getSettlementBasis()
	_ = e.getInitialCapital()

	// 启动时校验组合每日结算数据是否一致/缺失
	e.validateSettlementOnStart()
	return nil
}

// validateSettlementOnStart 启动时校验每日结算数据并记录日志
func (e *Engine) validateSettlementOnStart() {
	issues := e.ValidateSettlement()
	if len(issues) == 0 {
		log.Println("[Portfolio] 结算数据校验通过：每日结算记录一致，无异常")
		return
	}
	log.Printf("[Portfolio] 结算数据校验发现问题(%d)：", len(issues))
	for _, it := range issues {
		log.Printf("  - %s", it)
	}
	// 数据缺失/不一致属于严重问题：此场景下 今日盈亏 将退化为 累计盈亏，需明确告警
	log.Println("[Portfolio] 提示：请确认每日盘后已由智能体调用 daily_settlement 工具完成当日结算")
}

// loadPrevCloseFromDB 从数据库加载前一日收盘价
// 强制使用最新 PositionSnapshot 的 ClosePrice 作为 PrevClose，确保今日盈亏计算准确
// loadPrevCloseFromDB 加载前一日收盘价（带内存缓存，避免重复查询）
func (e *Engine) loadPrevCloseFromDB(snapshot *PortfolioSnapshot) {
	if e.db == nil {
		return
	}

	// 1. 尝试使用缓存（已加载过则直接使用）
	e.mu.RLock()
	cacheLoaded := e.prevCloseCacheLoaded
	cache := e.prevCloseCache
	e.mu.RUnlock()

	if cacheLoaded {
		// 使用缓存
		for _, pos := range snapshot.Positions {
			if prevClose, ok := cache[pos.InstrumentID]; ok && prevClose > 0 {
				pos.PrevClose = prevClose
			}
		}
		return
	}

	// 2. 缓存未加载，查询数据库
	// 收集需要加载 PrevClose 的持仓
	var missing []*PositionState
	for _, pos := range snapshot.Positions {
		missing = append(missing, pos)
	}
	if len(missing) == 0 {
		return
	}

	// 批量查询这些持仓的前一日快照（排除今天的快照）
	today := time.Now().Format("2006-01-02")
	instrumentIDs := make([]string, 0, len(missing))
	for _, pos := range missing {
		instrumentIDs = append(instrumentIDs, pos.InstrumentID)
	}
	var allSnapshots []data.PositionSnapshot
	e.db.GetDB().
		Where("portfolio_id = ? AND instrument_id IN ? AND snapshot_date < ?", e.portfolioID, instrumentIDs, today).
		Order("snapshot_date DESC").
		Find(&allSnapshots)
	latestByInstrument := make(map[string]data.PositionSnapshot, len(missing))
	for _, snap := range allSnapshots {
		if _, ok := latestByInstrument[snap.InstrumentID]; !ok {
			latestByInstrument[snap.InstrumentID] = snap
		}
	}

	// 更新缓存和持仓
	newCache := make(map[string]float64)
	for _, pos := range missing {
		snap, ok := latestByInstrument[pos.InstrumentID]
		if ok && snap.ClosePrice > 0 {
			if pos.PrevClose != snap.ClosePrice {
				log.Printf("[Portfolio] Updated PrevClose from DB for %s: %.2f -> %.2f (snapshot date: %s)",
					pos.InstrumentID, pos.PrevClose, snap.ClosePrice, snap.SnapshotDate)
			}
			pos.PrevClose = snap.ClosePrice
			newCache[pos.InstrumentID] = snap.ClosePrice
		} else {
			log.Printf("[Portfolio] No previous snapshot found in DB for %s, keeping current PrevClose: %.2f", pos.InstrumentID, pos.PrevClose)
		}
	}

	// 更新缓存
	e.mu.Lock()
	for k, v := range newCache {
		e.prevCloseCache[k] = v
	}
	e.prevCloseCacheLoaded = true
	e.mu.Unlock()
}

// GetPositions 获取持仓明细（JSON友好格式）
func (e *Engine) GetPositions() map[string]interface{} {
	snapshot := e.GetSnapshot()

	positions := make([]map[string]interface{}, 0, len(snapshot.Positions))
	for _, pos := range snapshot.Positions {
		positions = append(positions, map[string]interface{}{
			"code":             pos.InstrumentID,
			"stockName":        e.resolveDisplayName(pos),
			"quantity":         pos.Quantity,
			"currentPrice":     pos.CurrentPrice,
			"avgCost":          pos.AvgCost,
			"marketValue":      pos.MarketValue,
			"unrealizedPnL":    pos.UnrealizedPnL,
			"unrealizedReturn": pos.UnrealizedReturn,
			"weight":           pos.Weight,
			"openDate":         pos.OpenDate.Format(time.RFC3339),
			"lastUpdate":       pos.LastUpdate.Format(time.RFC3339),
		})
	}

	return map[string]interface{}{
		"positions":   positions,
		"totalCount":  len(positions),
		"cash":        snapshot.Cash,
		"totalAssets": snapshot.Cash + snapshot.TotalMarketValue,
	}
}

// GetContinuousStatus 获取持续交易状态
func (e *Engine) GetContinuousStatus() map[string]interface{} {
	e.mu.RLock()
	defer e.mu.RUnlock()

	// 检查是否在交易时间
	inTradingTime := isTradingTime()

	// 如果在交易时间外，running 状态应该反映实际交易状态
	// 系统可能处于运行状态（监控中），但不在交易时段
	actualRunning := e.running && inTradingTime

	return map[string]interface{}{
		"running":       actualRunning, // 实际交易状态：仅在交易时段为 true
		"monitoring":    e.running,     // 监控状态：系统是否在运行
		"inTradingTime": inTradingTime, // 是否在交易时间
		"portfolioID":   e.portfolioID,
		"cash":          e.cash,
		"positions":     len(e.positions),
		"lastUpdate":    time.Now().Format(time.RFC3339),
	}
}

// RefreshPrices 刷新所有持仓的价格
// 使用短 TTL 缓存：缓存未过期时直接返回，避免页面加载/连续监控重复网络请求
func (e *Engine) RefreshPrices() []data.StockSnapshot {
	e.mu.RLock()
	if e.quoteCache != nil && time.Since(e.quoteCacheTime) < quoteCacheTTL {
		cached := e.quoteCache
		e.mu.RUnlock()
		return cached
	}
	// 非交易时段且已加载过行情缓存：直接复用（收盘/最新）价格，
	// 避免收盘后 GetSnapshot 等路径每隔 quoteCacheTTL 反复拉取实时行情、
	// 反复触发 UpdatePrices/净持仓对账，造成"收盘后循环执行"的空转。
	if !isTradingTime() && e.quoteCache != nil {
		cached := e.quoteCache
		e.mu.RUnlock()
		return cached
	}
	var codes []string
	for id := range e.positions {
		codes = append(codes, id)
	}
	e.mu.RUnlock()

	if len(codes) == 0 {
		return nil
	}

	snapshots, _ := data.FetchRealtimeStockSnapshots(codes)
	if len(snapshots) > 0 {
		e.UpdatePrices(snapshots)
		e.mu.Lock()
		e.quoteCache = snapshots
		e.quoteCacheTime = time.Now()
		e.mu.Unlock()
	}
	return snapshots
}

// RecordDailySnapshot 记录每日持仓快照和组合统计
func (e *Engine) RecordDailySnapshot() error {
	if e.db == nil {
		return fmt.Errorf("database not initialized")
	}

	snapshot := e.GetSnapshot()
	today := time.Now().Format("2006-01-02")

	// 1. 记录每只股票的持仓快照
	positionSnapshots := make([]data.PositionSnapshot, 0, len(snapshot.Positions))
	for _, pos := range snapshot.Positions {
		dailyPnL := 0.0
		dailyReturn := 0.0
		if pos.PrevClose > 0 {
			dailyPnL = (pos.CurrentPrice - pos.PrevClose) * float64(pos.Quantity)
			dailyCostBasis := pos.PrevClose * float64(pos.Quantity)
			if dailyCostBasis > 0 {
				dailyReturn = dailyPnL / dailyCostBasis * 100
			}
		}

		positionSnapshots = append(positionSnapshots, data.PositionSnapshot{
			SnapshotDate:     today,
			PortfolioID:      e.portfolioID,
			InstrumentID:     pos.InstrumentID,
			StockName:        pos.StockName,
			Market:           pos.Market,
			Quantity:         pos.Quantity,
			ClosePrice:       pos.CurrentPrice,
			PrevClose:        pos.PrevClose,
			MarketValue:      pos.MarketValue,
			UnrealizedPnL:    pos.UnrealizedPnL,
			UnrealizedReturn: pos.UnrealizedReturn,
			DailyPnL:         dailyPnL,
			DailyReturn:      dailyReturn,
			Weight:           pos.Weight,
		})
	}

	// 2. 删除当日已有快照（避免重复）
	e.db.GetDB().Where("snapshot_date = ? AND portfolio_id = ?", today, e.portfolioID).
		Delete(&data.PositionSnapshot{})

	// 3. 批量创建新快照
	if len(positionSnapshots) > 0 {
		if err := e.db.GetDB().CreateInBatches(positionSnapshots, 50).Error; err != nil {
			return fmt.Errorf("failed to create position snapshots: %w", err)
		}
	}

	// 4. 记录组合每日统计
	// 获取当日交易数据
	var tradeCount int64
	var totalDailyVolume float64
	e.db.GetDB().Model(&data.Trade{}).
		Where("portfolio_id = ? AND DATE(trade_date) = ?", e.portfolioID, today).
		Count(&tradeCount)

	var trades []data.Trade
	e.db.GetDB().Where("portfolio_id = ? AND DATE(trade_date) = ?", e.portfolioID, today).
		Find(&trades)
	for _, t := range trades {
		totalDailyVolume += t.GrossAmount
	}

	// 结算口径（数据驱动，与 GetPortfolioState 一致）：
	//   TotalAssets = 现金 + 持仓价值
	//   累计盈亏 = TotalAssets - 期初资产(InitialCapital)
	//   今日盈亏 = TotalAssets - 昨日(基准)总资产
	totalAssets := snapshot.Cash + snapshot.TotalMarketValue
	initialCapital := e.getInitialCapital()
	basisAssets, _ := e.getSettlementBasis()
	totalPnL := totalAssets - initialCapital
	totalReturn := 0.0
	if initialCapital > 0 {
		totalReturn = totalPnL / initialCapital * 100
	}
	dailyPnL := totalAssets - basisAssets
	dailyReturn := 0.0
	if basisAssets > 0 {
		dailyReturn = dailyPnL / basisAssets * 100
	}

	dailyStat := data.PortfolioDailyStat{
		StatDate:         today,
		PortfolioID:      e.portfolioID,
		TotalAssets:      totalAssets,
		TotalCapital:     initialCapital,
		Cash:             snapshot.Cash,
		MarketValue:      snapshot.TotalMarketValue,
		TotalPnL:         totalPnL,
		TotalReturn:      totalReturn,
		DailyPnL:         dailyPnL,
		DailyReturn:      dailyReturn,
		PositionsCount:   len(snapshot.Positions),
		TotalDailyVolume: totalDailyVolume,
		TradeCount:       int(tradeCount),
	}

	// Upsert: 按日期更新或创建
	var existingStat data.PortfolioDailyStat
	result := e.db.GetDB().Where("stat_date = ? AND portfolio_id = ?", today, e.portfolioID).
		First(&existingStat)
	if result.Error == nil {
		existingStat.TotalAssets = dailyStat.TotalAssets
		existingStat.Cash = dailyStat.Cash
		existingStat.MarketValue = dailyStat.MarketValue
		existingStat.TotalPnL = dailyStat.TotalPnL
		existingStat.TotalReturn = dailyStat.TotalReturn
		existingStat.DailyPnL = dailyStat.DailyPnL
		existingStat.DailyReturn = dailyStat.DailyReturn
		existingStat.PositionsCount = dailyStat.PositionsCount
		existingStat.TotalDailyVolume = dailyStat.TotalDailyVolume
		existingStat.TradeCount = dailyStat.TradeCount
		if err := e.db.GetDB().Save(&existingStat).Error; err != nil {
			return fmt.Errorf("failed to update portfolio daily stat: %w", err)
		}
	} else {
		if err := e.db.GetDB().Create(&dailyStat).Error; err != nil {
			return fmt.Errorf("failed to create portfolio daily stat: %w", err)
		}
	}

	log.Printf("[Portfolio] Daily snapshot recorded: date=%s, positions=%d, totalAssets=%.2f, dailyPnL=%.2f",
		today, len(snapshot.Positions), dailyStat.TotalAssets, dailyStat.DailyPnL)

	// 月度报告自动生成：每月首次(1日)结算后，自动生成上个月的投资者月度报告。
	_tNow := time.Now()
	if _tNow.Day() == 1 {
		prev := _tNow.AddDate(0, -1, 0)
		key := prev.Format("2006-01")
		if e.autoReportMonth != key {
			if path, rErr := e.GenerateMonthlyReport(prev.Year(), int(prev.Month()), ""); rErr == nil {
				e.autoReportMonth = key
				log.Printf("[Portfolio] 已自动生成月度报告: %s", path)
			} else {
				log.Printf("[Portfolio] 自动生成月度报告失败: %v", rErr)
			}
		}
	}

	return nil
}

// GetProfitHistory 获取历史收益数据（用于图表展示）
func (e *Engine) GetProfitHistory(days int) ([]map[string]interface{}, error) {
	if e.db == nil {
		return nil, fmt.Errorf("database not initialized")
	}

	if days <= 0 {
		days = 30
	}

	var stats []data.PortfolioDailyStat
	startDate := time.Now().AddDate(0, 0, -days).Format("2006-01-02")
	endDate := time.Now().Format("2006-01-02")

	e.db.GetDB().
		Where("portfolio_id = ? AND stat_date >= ? AND stat_date <= ?", e.portfolioID, startDate, endDate).
		Order("stat_date ASC").
		Find(&stats)

	result := make([]map[string]interface{}, len(stats))
	for i, s := range stats {
		result[i] = map[string]interface{}{
			"date":           s.StatDate,
			"totalAssets":    s.TotalAssets,
			"totalPnL":       s.TotalPnL,
			"totalReturn":    s.TotalReturn,
			"dailyPnL":       s.DailyPnL,
			"dailyReturn":    s.DailyReturn,
			"cash":           s.Cash,
			"marketValue":    s.MarketValue,
			"positionsCount": s.PositionsCount,
		}
	}

	return result, nil
}

// GenerateMonthlyReport 生成一份投资者月度报告（Markdown）并写入目录 dir，
// 返回报告文件绝对路径。报告含：月末净值与月度收益、月度最大回撤、月度交易统计、
// 期末持仓明细、逐日净值表。year/month 缺省（<=0）时取最近一个自然月。
func (e *Engine) GenerateMonthlyReport(year, month int, dir string) (string, error) {
	if e.db == nil {
		return "", fmt.Errorf("database not initialized")
	}
	now := time.Now()
	if year <= 0 || month <= 0 || month > 12 {
		prev := now.AddDate(0, -1, 0)
		year, month = prev.Year(), int(prev.Month())
	}
	monthStart := time.Date(year, time.Month(month), 1, 0, 0, 0, 0, time.Local)
	monthEnd := monthStart.AddDate(0, 1, -1)
	startISO := monthStart.Format("2006-01-02")
	endISO := monthEnd.Format("2006-01-02")
	todayISO := now.Format("2006-01-02")

	gdb := e.db.GetDB()

	// 1) 月度逐日结算
	var stats []data.PortfolioDailyStat
	gdb.Where("portfolio_id = ? AND stat_date >= ? AND stat_date <= ?", e.portfolioID, startISO, endISO).
		Order("stat_date ASC").Find(&stats)

	// 2) 月度成交
	var trades []data.Trade
	gdb.Where("portfolio_id = ? AND trade_date >= ? AND trade_date <= ?",
		e.portfolioID, startISO+" 00:00:00", todayISO+" 23:59:59").Find(&trades)

	// 3) 期末持仓
	snap := e.GetSnapshot()

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("# QuantBot 月度投资报告 — %d年%d月\n\n", year, month))
	sb.WriteString(fmt.Sprintf("> 生成时间：%s　组合账户：P%03d\n\n", now.Format("2006-01-02 15:04:05"), e.portfolioID))

	// —— 月度概览 ——
	var first, last float64
	if len(stats) > 0 {
		first, last = stats[0].TotalAssets, stats[len(stats)-1].TotalAssets
	}
	sb.WriteString("## 一、月度概览\n\n")
	sb.WriteString("| 指标 | 数值 |\n| --- | --- |\n")
	if len(stats) > 0 {
		sb.WriteString(fmt.Sprintf("| 期初总资产 | ¥%.2f |\n", first))
		sb.WriteString(fmt.Sprintf("| 期末总资产 | ¥%.2f |\n", last))
		if first > 0 {
			sb.WriteString(fmt.Sprintf("| 月度收益率 | %+.2f%% |\n", (last-first)/first*100))
		}
		sb.WriteString(fmt.Sprintf("| 期末现金 | ¥%.2f |\n", snap.Cash))
		sb.WriteString(fmt.Sprintf("| 期末持仓市值 | ¥%.2f |\n", snap.TotalMarketValue))
		sb.WriteString(fmt.Sprintf("| 期末盈亏(累计) | %+.2f%% |\n", snap.TotalReturn*100))
	} else {
		sb.WriteString("| 期初/期末总资产 | 本月暂无结算数据 |\n")
	}
	sb.WriteString("\n")

	// —— 月度最大回撤 ——
	sb.WriteString("## 二、月度最大回撤\n\n")
	if len(stats) > 1 {
		peak := stats[0].TotalAssets
		maxDd := 0.0
		for _, s := range stats[1:] {
			if s.TotalAssets > peak {
				peak = s.TotalAssets
			} else if peak > 0 {
				dd := (peak - s.TotalAssets) / peak * 100
				if dd > maxDd {
					maxDd = dd
				}
			}
		}
		sb.WriteString(fmt.Sprintf("本月组合净值相对月初高点的最大回撤为 **%.2f%%**\n\n", maxDd))
	} else {
		sb.WriteString("结算数据不足，暂无法计算月度最大回撤。\n\n")
	}

	// —— 月度交易统计 ——
	sb.WriteString("## 三、月度交易统计\n\n")
	buyCnt, sellCnt := 0, 0
	buyAmt, sellAmt, totalFee := 0.0, 0.0, 0.0
	for _, t := range trades {
		totalFee += t.Commission + t.Fees
		if t.Side == "BUY" {
			buyCnt++
			buyAmt += t.NetAmount
		} else {
			sellCnt++
			sellAmt += t.NetAmount
		}
	}
	sb.WriteString("| 项目 | 笔数 | 金额 |\n| --- | --- | --- |\n")
	sb.WriteString(fmt.Sprintf("| 买入 | %d | ¥%.2f |\n", buyCnt, buyAmt))
	sb.WriteString(fmt.Sprintf("| 卖出 | %d | ¥%.2f |\n", sellCnt, sellAmt))
	sb.WriteString(fmt.Sprintf("| 手续费合计 | — | ¥%.2f |\n\n", totalFee))

	// —— 期末持仓 ——
	sb.WriteString("## 四、期末持仓\n\n")
	if len(snap.Positions) == 0 {
		sb.WriteString("当前无持仓。\n\n")
	} else {
		sb.WriteString("| 代码 | 名称 | 股数 | 成本 | 现价 | 市值 | 盈亏 |\n| --- | --- | --- | --- | --- | --- | --- |\n")
		for _, p := range snap.Positions {
			sb.WriteString(fmt.Sprintf("| %s | %s | %d | %.2f | %.2f | %.2f | %+.2f%% |\n",
				p.InstrumentID, p.StockName, p.Quantity, p.AvgCost, p.CurrentPrice,
				float64(p.Quantity)*p.CurrentPrice, p.UnrealizedReturn*100))
		}
		sb.WriteString("\n")
	}

	// —— 逐日净值表 ——
	sb.WriteString("## 五、逐日净值表\n\n")
	if len(stats) == 0 {
		sb.WriteString("本月暂无结算数据。\n\n")
	} else {
		base := stats[0].TotalAssets
		sb.WriteString("| 日期 | 总资产 | 日收益率 | 累计收益率 |\n| --- | --- | --- | --- |\n")
		for _, s := range stats {
			sb.WriteString(fmt.Sprintf("| %s | ¥%.2f | %+.2f%% | %+.2f%% |\n",
				s.StatDate, s.TotalAssets, s.DailyReturn*100, s.TotalReturn*100))
		}
		sb.WriteString(fmt.Sprintf("\n> 说明：累计收益率基于期初总资产%.2f基准计算。\n", base))
	}
	sb.WriteString("\n—— 本报告由 QuantBot 自动生成，仅供内部管理参考 ——\n")

	if dir == "" {
		exePath, err := os.Executable()
		if err != nil {
			return "", fmt.Errorf("无法获取可执行文件路径: %w", err)
		}
		dir = filepath.Join(filepath.Dir(exePath), "reports")
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", err
	}
	fileName := fmt.Sprintf("monthly_%04d-%02d.md", year, month)
	filePath := filepath.Join(dir, fileName)
	if err := os.WriteFile(filePath, []byte(sb.String()), 0644); err != nil {
		return "", err
	}
	return filePath, nil
}
func (e *Engine) GetPositionSnapshots(startDate, endDate string) ([]map[string]interface{}, error) {
	if e.db == nil {
		return nil, fmt.Errorf("database not initialized")
	}

	var snapshots []data.PositionSnapshot
	e.db.GetDB().
		Where("portfolio_id = ? AND snapshot_date >= ? AND snapshot_date <= ?", e.portfolioID, startDate, endDate).
		Order("snapshot_date ASC, instrument_id ASC").
		Find(&snapshots)

	result := make([]map[string]interface{}, len(snapshots))
	for i, s := range snapshots {
		result[i] = map[string]interface{}{
			"date":             s.SnapshotDate,
			"instrumentID":     s.InstrumentID,
			"stockName":        s.StockName,
			"quantity":         s.Quantity,
			"closePrice":       s.ClosePrice,
			"prevClose":        s.PrevClose,
			"marketValue":      s.MarketValue,
			"unrealizedPnL":    s.UnrealizedPnL,
			"unrealizedReturn": s.UnrealizedReturn,
			"dailyPnL":         s.DailyPnL,
			"dailyReturn":      s.DailyReturn,
		}
	}

	return result, nil
}

// GetLatestDailyStat 获取最新的每日统计
func (e *Engine) GetLatestDailyStat() (*data.PortfolioDailyStat, error) {
	if e.db == nil {
		return nil, fmt.Errorf("database not initialized")
	}

	var stat data.PortfolioDailyStat
	result := e.db.GetDB().
		Where("portfolio_id = ?", e.portfolioID).
		Order("stat_date DESC").
		First(&stat)
	if result.Error != nil {
		return nil, result.Error
	}

	return &stat, nil
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// MaxHistoricalAssets 返回组合历史上最高的总资产（含当前实时口径），供回撤熔断计算峰值。
// 峰值 = max(历史日结算总资产, 当前总资产=现金+持仓市值)，避免数据缺口时的伪回撤。
func (e *Engine) MaxHistoricalAssets() float64 {
	if e.db == nil {
		return 0
	}
	snap := e.GetSnapshot()
	var peak float64
	if snap != nil {
		peak = snap.Cash + snap.TotalMarketValue
	}
	var histPeak float64
	e.db.GetDB().Model(&data.PortfolioDailyStat{}).
		Where("portfolio_id = ?", e.portfolioID).
		Select("COALESCE(MAX(total_assets),0)").Scan(&histPeak)
	if histPeak > peak {
		peak = histPeak
	}
	return peak
}

// writeTradeJSON 将交易记录输出到JSON文件
func (e *Engine) writeTradeJSON(trade *TradeRecord) error {
	// 根据交易模式确定子目录
	subDir := e.tradingMode
	dir := filepath.Join(e.outputDir, subDir)
	os.MkdirAll(dir, 0755)

	// 生成文件名：日期_交易ID.json
	today := trade.TradeDate.Format("20060102")
	filename := fmt.Sprintf("%s_%s.json", today, trade.TradeID)
	filepath := filepath.Join(dir, filename)

	var data interface{}

	switch e.tradingMode {
	case TradingModeSimulated:
		// 模拟接口：输出完整的交易结果
		data = map[string]interface{}{
			"mode":          "simulated",
			"trade_id":      trade.TradeID,
			"side":          trade.Side,
			"instrument_id": trade.InstrumentID,
			"stock_name":    trade.StockName,
			"quantity":      trade.Quantity,
			"price":         trade.Price,
			"gross_amount":  trade.GrossAmount,
			"commission":    trade.Commission,
			"fees":          trade.Fees,
			"net_amount":    trade.NetAmount,
			"realized_pnl":  trade.RealizedPnL,
			"trade_date":    trade.TradeDate.Format(time.RFC3339),
			"reason":        trade.Reason,
			"status":        "executed",
		}

	case TradingModeLive:
		// 实盘交易：输出券商接口JSON指令
		data = map[string]interface{}{
			"mode":         "live",
			"command_type": "broker_order",
			"order": map[string]interface{}{
				"order_id":      trade.TradeID,
				"order_type":    "LIMIT",
				"side":          e.convertSide(trade.Side),
				"instrument_id": trade.InstrumentID,
				"stock_name":    trade.StockName,
				"quantity":      trade.Quantity,
				"price":         trade.Price,
				"account_id":    "default",
				"market":        e.getMarketForTrade(trade.InstrumentID),
				"currency":      "CNY",
				"time_in_force": "DAY",
				"reason":        trade.Reason,
			},
			"metadata": map[string]interface{}{
				"portfolio_id": e.portfolioID,
				"gross_amount": trade.GrossAmount,
				"commission":   trade.Commission,
				"fees":         trade.Fees,
				"net_amount":   trade.NetAmount,
				"realized_pnl": trade.RealizedPnL,
				"generated_at": trade.TradeDate.Format(time.RFC3339),
				"system":       "QuantBot AI Trading System",
				"version":      "v1.0",
			},
		}

	default:
		return fmt.Errorf("unknown trading mode: %s", e.tradingMode)
	}

	// 写入JSON文件
	jsonData, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal trade JSON: %w", err)
	}

	if err := os.WriteFile(filepath, jsonData, 0644); err != nil {
		return fmt.Errorf("failed to write trade JSON file: %w", err)
	}

	log.Printf("[Portfolio] Trade JSON written: %s (mode: %s)", filepath, e.tradingMode)
	return nil
}

// convertSide 将内部交易方向转换为券商接口格式
func (e *Engine) convertSide(side string) string {
	switch side {
	case "BUY":
		return "BUY"
	case "SELL":
		return "SELL"
	default:
		return side
	}
}

// getMarketForTrade 根据证券代码推断市场
func (e *Engine) getMarketForTrade(code string) string {
	if len(code) < 3 {
		return "UNKNOWN"
	}
	prefix := code[:3]
	switch {
	case prefix == "000" || prefix == "001" || prefix == "002" || prefix == "003" ||
		prefix == "200" || prefix == "300" || prefix == "301":
		return "SZ" // 深市
	case prefix == "430" || prefix == "831" || prefix == "870" || prefix == "871" || prefix == "872" || prefix == "873":
		return "BJ" // 北交所
	default:
		return "SH" // 沪市
	}
}

// GetRecentTradeJSONFiles 获取最近的交易JSON文件列表
func (e *Engine) GetRecentTradeJSONFiles(n int) ([]map[string]interface{}, error) {
	dir := filepath.Join(e.outputDir, e.tradingMode)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("failed to read trade directory: %w", err)
	}

	type fileInfo struct {
		name    string
		modTime time.Time
	}

	var files []fileInfo
	for _, entry := range entries {
		if !entry.IsDir() && filepath.Ext(entry.Name()) == ".json" {
			info, err := entry.Info()
			if err != nil {
				continue
			}
			files = append(files, fileInfo{
				name:    entry.Name(),
				modTime: info.ModTime(),
			})
		}
	}

	// 按修改时间排序（最新的在前）
	sort.Slice(files, func(i, j int) bool {
		return files[i].modTime.After(files[j].modTime)
	})

	// 取前n个
	if n > len(files) {
		n = len(files)
	}
	files = files[:n]

	result := make([]map[string]interface{}, 0, len(files))
	for _, f := range files {
		filePath := filepath.Join(dir, f.name)
		data, err := os.ReadFile(filePath)
		if err != nil {
			continue
		}

		var tradeData interface{}
		if err := json.Unmarshal(data, &tradeData); err != nil {
			continue
		}

		result = append(result, map[string]interface{}{
			"filename":   f.name,
			"path":       filePath,
			"modified":   f.modTime.Format(time.RFC3339),
			"trade_data": tradeData,
		})
	}

	return result, nil
}
