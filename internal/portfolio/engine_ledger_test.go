package portfolio

import (
	"testing"
	"time"

	"github.com/quantpilot/quantpilot/internal/data"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// newLedgerTestEngine 构造一个内存 SQLite 的 Engine，模拟「期初 + 3 笔买入成交」的记账场景，
// 用于验证财务流水账对账（现金由明细账推导 / 四账对平）。
func newLedgerTestEngine(t *testing.T) *Engine {
	t.Helper()
	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("open memory db: %v", err)
	}
	if err := gdb.AutoMigrate(
		&data.Portfolio{},
		&data.Position{},
		&data.Order{},
		&data.Trade{},
		&data.PositionSnapshot{},
		&data.PortfolioDailyStat{},
	); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	// 期初资金 100,000
	portfolio := data.Portfolio{
		Name:           "AI量化小基金",
		PortfolioType:  "MANAGED",
		InitialCapital: 100000,
		CurrentCapital: 99712,
		Cash:           60694,
		IsActive:       1,
	}
	if err := gdb.Create(&portfolio).Error; err != nil {
		t.Fatalf("create portfolio: %v", err)
	}

	// 明细账：3 笔买入成交（净额 = 成交金额 + 佣金）
	now := time.Now()
	trades := []data.Trade{
		{TradeID: "T1", OrderID: "O1", PortfolioID: portfolio.ID, InstrumentID: "600061", Side: "BUY", Quantity: 1700, Price: 6.57, GrossAmount: 11169, Commission: 5, Fees: 0, NetAmount: 11174, TradeDate: now},
		{TradeID: "T2", OrderID: "O2", PortfolioID: portfolio.ID, InstrumentID: "600183", Side: "BUY", Quantity: 100, Price: 140.2, GrossAmount: 14020, Commission: 5, Fees: 0, NetAmount: 14025, TradeDate: now},
		{TradeID: "T3", OrderID: "O3", PortfolioID: portfolio.ID, InstrumentID: "600183", Side: "BUY", Quantity: 100, Price: 141.02, GrossAmount: 14102, Commission: 5, Fees: 0, NetAmount: 14107, TradeDate: now},
	}
	for i := range trades {
		if err := gdb.Create(&trades[i]).Error; err != nil {
			t.Fatalf("create trade: %v", err)
		}
	}

	eng := &Engine{
		db:             data.NewSQLiteManagerFromDB(gdb),
		portfolioID:    portfolio.ID,
		positions:      make(map[string]*PositionState),
		cash:           60694,
		totalCapital:   100000,
		reservedBuy:    0,
		prevCloseCache: make(map[string]float64),
	}
	return eng
}

// TestLedgerReconcileCashDerivedFromTrades 验证：现金按明细账推导（期初+Σ卖净−Σ买净），
// 且与内存/DB 现金一致，四账对平。
func TestLedgerReconcileCashDerivedFromTrades(t *testing.T) {
	eng := newLedgerTestEngine(t)

	// 对账：持仓 + 现金均由明细账推导
	eng.reconcilePositionsFromTrades()

	// 期望现金 = 100000 − (11174+14025+14107) = 60694
	if eng.cash != 60694 {
		t.Fatalf("reconcile cash = %.2f, want 60694", eng.cash)
	}
	// 持仓数量：600061=1700, 600183=200
	if p, ok := eng.positions["600061"]; !ok || p.Quantity != 1700 {
		t.Fatalf("600061 position missing/wrong: %+v", p)
	}
	if p, ok := eng.positions["600183"]; !ok || p.Quantity != 200 {
		t.Fatalf("600183 position missing/wrong: %+v", p)
	}

	// 现金写回 DB portfolios.cash
	var p data.Portfolio
	if err := eng.db.GetDB().First(&p, eng.portfolioID).Error; err != nil {
		t.Fatalf("read portfolio: %v", err)
	}
	if p.Cash != 60694 {
		t.Fatalf("db cash = %.2f, want 60694", p.Cash)
	}
}

// TestLedgerSummaryReconciled 验证四账对账汇总：期初账/临时账/明细账/总账对平。
func TestLedgerSummaryReconciled(t *testing.T) {
	eng := newLedgerTestEngine(t)

	// 补一条临时账：1 笔 pending 挂单占用（模拟挂单未确认）
	_ = eng.db.GetDB().Create(&data.Order{
		OrderID:      "ta_pending1",
		PortfolioID:  eng.portfolioID,
		InstrumentID: "600061",
		OrderType:    "MARKET",
		Side:         "BUY",
		Quantity:     100,
		Price:        6.5,
		Status:       "pending",
	})

	summary := eng.LedgerSummary()

	if !summary["reconciled"].(bool) {
		t.Fatalf("ledger should be reconciled, issues=%v", summary["issues"])
	}
	if v := summary["opening_capital"].(float64); v != 100000 {
		t.Fatalf("opening_capital = %.2f, want 100000", v)
	}
	cashMap := summary["cash"].(map[string]interface{})
	if v := cashMap["derived"].(float64); v != 60694 {
		t.Fatalf("derived cash = %.2f, want 60694", v)
	}
	detail := summary["detail"].(map[string]interface{})
	if v := detail["trade_count"].(int); v != 3 {
		t.Fatalf("trade_count = %d, want 3", v)
	}
	if v := detail["buy_count"].(int); v != 3 {
		t.Fatalf("buy_count = %d, want 3", v)
	}
}
