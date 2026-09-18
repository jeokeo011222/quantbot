package brainhost

import (
	"context"

	"github.com/quantpilot/quantpilot/internal/data"
	"github.com/quantpilot/quantpilot/internal/marketinfo"
	"github.com/quantpilot/quantpilot/internal/port"
	"github.com/quantpilot/quantpilot/internal/thssdk"
)

// ==================== MarketDataStore 适配（对齐 data.DuckDBManager） ====================

// marketDataAdapter 将宿主 internal/data.DuckDBManager 适配为 port.MarketDataStore。
type marketDataAdapter struct {
	inner *data.DuckDBManager
}

// AdaptMarketDataStore 将 data.DuckDBManager 包装为 port.MarketDataStore。
func AdaptMarketDataStore(dm *data.DuckDBManager) port.MarketDataStore {
	return &marketDataAdapter{inner: dm}
}

func (a *marketDataAdapter) HasStockDB() bool {
	if a.inner == nil {
		return false
	}
	return a.inner.HasStockDB()
}

func (a *marketDataAdapter) GetKlineFromStock(ctx context.Context, symbol string, days int) ([]port.KlineBar, error) {
	bars, err := a.inner.GetKlineFromStock(ctx, symbol, days)
	if err != nil {
		return nil, err
	}
	out := make([]port.KlineBar, len(bars))
	for i, b := range bars {
		out[i] = port.KlineBar{
			Symbol: b.Symbol, Date: b.Date, Open: b.Open, High: b.High,
			Low: b.Low, Close: b.Close, Volume: b.Volume, Amount: b.Amount,
		}
	}
	return out, nil
}

func (a *marketDataAdapter) ListRepresentativeSymbols(ctx context.Context, n int) ([]port.SymbolInfo, error) {
	list, err := a.inner.ListRepresentativeSymbols(ctx, n)
	if err != nil {
		return nil, err
	}
	return symbolInfosToPort(list), nil
}

func (a *marketDataAdapter) ListAllSymbolsFromStock(ctx context.Context, market string, limit int) ([]port.SymbolInfo, error) {
	list, err := a.inner.ListAllSymbolsFromStock(ctx, market, limit)
	if err != nil {
		return nil, err
	}
	return symbolInfosToPort(list), nil
}

func symbolInfosToPort(list []data.StockSymbolInfo) []port.SymbolInfo {
	out := make([]port.SymbolInfo, len(list))
	for i, s := range list {
		out[i] = port.SymbolInfo{Symbol: s.Symbol, Name: s.Name, Market: s.Market}
	}
	return out
}

func (a *marketDataAdapter) BatchGetFactorBars(ctx context.Context, symbols []string, days int) (map[string][]port.FactorBar, error) {
	m, err := a.inner.BatchGetFactorBars(ctx, symbols, days)
	if err != nil {
		return nil, err
	}
	out := make(map[string][]port.FactorBar, len(m))
	for k, bars := range m {
		converted := make([]port.FactorBar, len(bars))
		for i, b := range bars {
			converted[i] = port.FactorBar{
				Symbol: b.Symbol, Date: b.Date, Open: b.Open, High: b.High,
				Low: b.Low, Close: b.Close, PreClose: b.PreClose, Volume: b.Volume,
				Amount: b.Amount, Turnover: b.Turnover, PctChg: b.PctChg, Amplitude: b.Amplitude,
			}
		}
		out[k] = converted
	}
	return out, nil
}

func (a *marketDataAdapter) GetSectorChangeStats(ctx context.Context) ([]port.SectorChangeStat, error) {
	list, err := a.inner.GetSectorChangeStats(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]port.SectorChangeStat, len(list))
	for i, s := range list {
		out[i] = port.SectorChangeStat{Sector: s.Sector, StockCount: s.StockCount, AvgChgPct: s.AvgChgPct}
	}
	return out, nil
}

func (a *marketDataAdapter) GetSectorPerformance(ctx context.Context) ([]port.SectorPerformanceRow, error) {
	rows, err := a.inner.GetSectorPerformance(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]port.SectorPerformanceRow, len(rows))
	for i, r := range rows {
		out[i] = port.SectorPerformanceRow(r)
	}
	return out, nil
}

func (a *marketDataAdapter) GetMarketAmountHistory(ctx context.Context, days int) ([]port.MarketAmountDay, error) {
	list, err := a.inner.GetMarketAmountHistory(ctx, days)
	if err != nil {
		return nil, err
	}
	out := make([]port.MarketAmountDay, len(list))
	for i, d := range list {
		out[i] = port.MarketAmountDay{Date: d.Date, Amount: d.Amount}
	}
	return out, nil
}

func (a *marketDataAdapter) GetMarketLimitStats(ctx context.Context) (*port.MarketLimitStats, error) {
	ls, err := a.inner.GetMarketLimitStats(ctx)
	if err != nil {
		return nil, err
	}
	return &port.MarketLimitStats{
		LimitUp:        ls.LimitUp,
		LimitDown:      ls.LimitDown,
		SealedLimitUp:  ls.SealedLimitUp,
		BlownUp:        ls.BlownUp,
		TouchedLimitUp: ls.TouchedLimitUp,
	}, nil
}

func (a *marketDataAdapter) SaveMarketSixDimRow(ctx context.Context, tradeDate, dimsJSON, sourcesJSON string, rawTotal, adjusted float64, conflict int, positionRate float64, tag string) error {
	return a.inner.SaveMarketSixDimRow(ctx, tradeDate, dimsJSON, sourcesJSON, rawTotal, adjusted, conflict, positionRate, tag)
}

func (a *marketDataAdapter) GetMarketSixDimRows(ctx context.Context, limit int) ([]port.MarketSixDimRow, error) {
	rows, err := data.GetMarketSixDimRows(ctx, a.inner, limit)
	if err != nil {
		return nil, err
	}
	out := make([]port.MarketSixDimRow, len(rows))
	for i, r := range rows {
		out[i] = port.MarketSixDimRow{
			TradeDate:          r.TradeDate,
			DimScoresJSON:      r.DimScoresJSON,
			RawTotalScore:      r.RawTotalScore,
			ConflictCount:      r.ConflictCount,
			AdjustedTotalScore: r.AdjustedTotalScore,
			PositionRate:       r.PositionRate,
			MarketTag:          r.MarketTag,
			SourcesJSON:        r.SourcesJSON,
			CreatedAt:          r.CreatedAt,
		}
	}
	return out, nil
}

// ==================== SnapSource 适配（对齐 data.UnifiedDataSource，动态取当前活跃 provider） ====================

// snapSourceAdapter 将 data.GetDataSource() 返回的当前活跃行情源适配为 port.SnapSource。
// 调用时动态读取全局当前 provider，保证用户在设置中切换数据源后六维判势即刻跟随。
type snapSourceAdapter struct{}

// AdaptSnapSource 返回从 data 全局活跃源的实时快照适配器。
func AdaptSnapSource() port.SnapSource {
	return &snapSourceAdapter{}
}

func (a *snapSourceAdapter) Source() string {
	if ds := data.GetDataSource(); ds != nil {
		return ds.Source()
	}
	return ""
}

func (a *snapSourceAdapter) GetStockSnapshots(codes []string) ([]port.StockSnapshot, error) {
	ds := data.GetDataSource()
	if ds == nil {
		return nil, nil
	}
	snaps, err := ds.GetStockSnapshots(codes)
	if err != nil {
		return nil, err
	}
	out := make([]port.StockSnapshot, len(snaps))
	for i, s := range snaps {
		out[i] = port.StockSnapshot{
			Code: s.Code, Name: s.Name, Market: s.Market, CurrentPrice: s.CurrentPrice,
			PrevClose: s.PrevClose, Open: s.Open, High: s.High, Low: s.Low,
			Volume: s.Volume, Turnover: s.Turnover, ChangePercent: s.ChangePercent,
			ChangeAmount: s.ChangeAmount, Timestamp: s.Timestamp, IsMock: s.IsMock,
		}
	}
	return out, nil
}

// ==================== MarketExternalSources 适配（对齐 internal/marketinfo 六维用函数） ====================

// externalSourcesAdapter 将 internal/marketinfo 的六维用函数适配为 port.MarketExternalSources。
type externalSourcesAdapter struct{}

// AdaptMarketExternalSources 返回 internal/marketinfo 六维外部源的适配器。
func AdaptMarketExternalSources() port.MarketExternalSources {
	return &externalSourcesAdapter{}
}

func (a *externalSourcesAdapter) FetchNorthboundRealtime(ctx context.Context) (float64, error) {
	net, _, err := marketinfo.FetchNorthboundRealtime(ctx)
	return net, err
}

func (a *externalSourcesAdapter) FetchMarginHistory(ctx context.Context, days int) ([]port.MarginPoint, error) {
	pts, _, err := marketinfo.FetchMarginHistory(ctx, days)
	if err != nil {
		return nil, err
	}
	out := make([]port.MarginPoint, len(pts))
	for i, p := range pts {
		out[i] = port.MarginPoint{Date: p.Date, Balance: p.Balance}
	}
	return out, nil
}

func (a *externalSourcesAdapter) FetchLimitUpBoard(ctx context.Context) (*port.LimitUpBoard, error) {
	board, _, err := marketinfo.FetchLimitUpBoard(ctx)
	if err != nil {
		return nil, err
	}
	return &port.LimitUpBoard{
		LimitUpCnt:   board.LimitUpCnt,
		LimitDownCnt: board.LimitDownCnt,
		ZhabanCnt:    board.ZhabanCnt,
	}, nil
}

func (a *externalSourcesAdapter) FetchExternalMarkets(ctx context.Context) ([]port.GlobalMarketPoint, string, error) {
	pts, src, err := marketinfo.FetchExternalMarkets(ctx)
	if err != nil {
		return nil, "", err
	}
	out := make([]port.GlobalMarketPoint, len(pts))
	for i, p := range pts {
		out[i] = port.GlobalMarketPoint{
			Code: p.Code, Name: p.Name, Current: p.Current, ChangePct: p.ChangePct, Change: p.Change,
		}
	}
	return out, src, nil
}

// ==================== THSSentimentSource 适配（对齐 internal/thssdk） ====================

// thsSentimentSourceAdapter 将 internal/thssdk 官方情绪面适配为 port.THSSentimentSource。
type thsSentimentSourceAdapter struct{}

// AdaptTHSSentimentSource 返回 internal/thssdk 官方情绪面源的适配器。
func AdaptTHSSentimentSource() port.THSSentimentSource {
	return &thsSentimentSourceAdapter{}
}

func (a *thsSentimentSourceAdapter) Enabled() bool {
	return thssdk.Default() != nil
}

func (a *thsSentimentSourceAdapter) FetchSentimentSnapshot(ctx context.Context) (*port.SentimentSnapshot, error) {
	c := thssdk.Default()
	if c == nil {
		return nil, nil
	}
	snap, err := thssdk.FetchSentimentSnapshot(ctx, c)
	if err != nil {
		return nil, err
	}
	return &port.SentimentSnapshot{
		LimitUpCnt:     snap.LimitUpCnt,
		LimitDownCnt:   snap.LimitDownCnt,
		BlowUpCnt:      snap.BlowUpCnt,
		BlowUpRate:     snap.BlowUpRate,
		MaxBoardHeight: snap.MaxBoardHeight,
	}, nil
}
