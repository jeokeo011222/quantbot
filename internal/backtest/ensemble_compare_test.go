package backtest

// 集成策略 vs 单 XGBoost 胜率对比回测。
// 方法论与策略指标刷新（RefreshStrategyMetrics）一致：样本池回测——
// 取多只真实流动性好的A股，各回测其全历史K线，聚合所有已平仓交易计算胜率。
// 仅信号生成不同（集成/单XGBoost），交易规则（止损止盈/涨跌停/滑点/佣金）完全一致，
// 因此胜率差异即模型差异。

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"testing"
	"time"

	_ "github.com/marcboeker/go-duckdb/v2"
	"github.com/quantpilot/quantpilot/internal/tdx"
)

// 对比路径（相对 internal/backtest 包测试的工作目录）
const (
	compareDBPath   = "../../build/bin/data/stock.duckdb"
	compareModelDir = "../../build/bin/models"
)

// ensembleNames 集成策略参与投票的模型（5 模型，含单XGBoost基准）
var ensembleNames = []string{
	"xgboost_astock_v1", "astock_lgbm_v1", "astock_rf_v1",
	"astock_logistic_v1", "astock_mlp_v1",
}

type aggResult struct {
	Wins        int
	Losses      int
	TotalTrades int
}

func (a *aggResult) WinRate() float64 {
	if a.TotalTrades == 0 {
		return 0
	}
	return float64(a.Wins) / float64(a.TotalTrades) * 100
}

func (a *aggResult) add(r *BacktestResult) {
	a.Wins += r.WinTrades
	a.Losses += r.LossTrades
	a.TotalTrades += r.TotalTrades
}

// TestEnsembleVsSingleXGBoost 样本池回测对比：5模型投票 / 5模型概率平均 / 单XGBoost
func TestEnsembleVsSingleXGBoost(t *testing.T) {
	if _, err := os.Stat(compareDBPath); err != nil {
		t.Skipf("行情库不存在，跳过对比: %v", err)
	}
	if _, err := os.Stat(compareModelDir); err != nil {
		t.Skipf("模型目录不存在，跳过对比: %v", err)
	}

	db, err := sql.Open("duckdb", compareDBPath)
	if err != nil {
		t.Fatalf("打开 DuckDB 失败: %v", err)
	}
	defer db.Close()

	stocks := pickCompareStocks(t, db)
	if len(stocks) < 5 {
		t.Fatalf("可用对比样本不足: %d", len(stocks))
	}
	t.Logf("对比样本池: %d 只股票 %v", len(stocks), stocks)

	strategies := []struct {
		label string
		build func() *MlModelStrategy
	}{
		{
			label: "集成(voting)",
			build: func() *MlModelStrategy {
				return &MlModelStrategy{
					ModelDir:       compareModelDir,
					ModelNames:     ensembleNames,
					EnsembleMethod: "voting",
					BuyThreshold:   0.5, SellThreshold: 0.3,
				}
			},
		},
		{
			label: "集成(probability)",
			build: func() *MlModelStrategy {
				return &MlModelStrategy{
					ModelDir:       compareModelDir,
					ModelNames:     ensembleNames,
					EnsembleMethod: "probability",
					BuyThreshold:   0.5, SellThreshold: 0.3,
				}
			},
		},
		{
			label: "单XGBoost",
			build: func() *MlModelStrategy {
				return &MlModelStrategy{
					ModelDir:     compareModelDir,
					ModelName:    "xgboost_astock_v1",
					BuyThreshold: 0.5, SellThreshold: 0.3,
				}
			},
		},
	}

	aggs := make([]*aggResult, len(strategies))
	for i := range aggs {
		aggs[i] = &aggResult{}
	}

	// 每只股票独立回测，三个策略共享同一份K线与交易规则
	for _, code := range stocks {
		bars, err := loadCompareBars(db, code, 1500)
		if err != nil || len(bars) < 60 {
			t.Logf("跳过样本 %s: %v", code, err)
			continue
		}
		cap := EnsureOneLotCapital(bars, 100000)
		for i, st := range strategies {
			strategy := st.build()
			res := RunBacktestWithLimitFin(bars, strategy, cap, 0.05, 0.20, code, "", nil)
			aggs[i].add(res)
			t.Logf("  %s %s: 交易=%d 胜=%d 负=%d", st.label, code, res.TotalTrades, res.WinTrades, res.LossTrades)
		}
	}

	// 汇总结果
	type row struct {
		Strategy    string  `json:"strategy"`
		WinRate     float64 `json:"win_rate"`
		Wins        int     `json:"wins"`
		Losses      int     `json:"losses"`
		TotalTrades int     `json:"total_trades"`
	}
	rows := make([]row, len(strategies))
	for i, st := range strategies {
		rows[i] = row{
			Strategy:    st.label,
			WinRate:     round2(aggs[i].WinRate()),
			Wins:        aggs[i].Wins,
			Losses:      aggs[i].Losses,
			TotalTrades: aggs[i].TotalTrades,
		}
		t.Logf("[对比] %s: 胜率=%.2f%% 胜=%d 负=%d 总交易=%d",
			st.label, aggs[i].WinRate(), aggs[i].Wins, aggs[i].Losses, aggs[i].TotalTrades)
	}

	if out, err := json.MarshalIndent(rows, "", "  "); err == nil {
		_ = os.WriteFile("ensemble_compare_result.json", out, 0644)
	}

	if aggs[0].TotalTrades == 0 || aggs[2].TotalTrades == 0 {
		t.Logf("样本成交不足，无法给出可靠结论（集成=%d 单XGB=%d）", aggs[0].TotalTrades, aggs[2].TotalTrades)
		return
	}
	t.Logf("集成(voting)胜率 %.2f%% vs 单XGBoost胜率 %.2f%%",
		aggs[0].WinRate(), aggs[2].WinRate())
}

// pickCompareStocks 从 ohlc 选取历史K线最长的 20 只主流A股（排除指数/北交所），保证样本足量。
func pickCompareStocks(t *testing.T, db *sql.DB) []string {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	query := `
		SELECT symbol, COUNT(*) AS c
		FROM ohlc
		WHERE (symbol LIKE 'sh60%' OR symbol LIKE 'sh68%' OR symbol LIKE 'sz00%' OR symbol LIKE 'sz30%')
		GROUP BY symbol
		HAVING c >= 800
		ORDER BY c DESC
		LIMIT 20
	`
	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		t.Fatalf("查询样本股票失败: %v", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var sym string
		var c int
		if err := rows.Scan(&sym, &c); err != nil {
			continue
		}
		out = append(out, sym)
	}
	return out
}

// loadCompareBars 读取单只股票最近 days 根K线并转升序（与 GetKlineFromStock 口径一致）
func loadCompareBars(db *sql.DB, symbol string, days int) ([]tdx.KlineBar, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	rows, err := db.QueryContext(ctx, `
		SELECT date, open, high, low, close, volume, amount
		FROM ohlc
		WHERE symbol = ?
		ORDER BY date DESC
		LIMIT ?
	`, symbol, days)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var bars []tdx.KlineBar
	for rows.Next() {
		var d time.Time
		var vol float64
		var b tdx.KlineBar
		if err := rows.Scan(&d, &b.Open, &b.High, &b.Low, &b.Close, &vol, &b.Amount); err != nil {
			return nil, err
		}
		b.Date = d.Format("2006-01-02")
		b.Volume = int64(vol)
		bars = append(bars, b)
	}
	// 倒序（新在前）→ 升序
	for i, j := 0, len(bars)-1; i < j; i, j = i+1, j-1 {
		bars[i], bars[j] = bars[j], bars[i]
	}
	return bars, nil
}

func round2(v float64) float64 {
	return float64(int(v*100+0.5)) / 100
}
