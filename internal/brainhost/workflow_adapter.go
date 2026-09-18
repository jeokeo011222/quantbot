package brainhost

import (
	"github.com/quantpilot/quantpilot/internal/brain/port"
	"github.com/quantpilot/quantpilot/internal/data"
)

// workflowStoreAdapter 将宿主 internal/data 的真实实现适配为决策脑抽象 port.WorkflowStore。
// 决策脑（internal/brain/agents.DailyCycle）只依赖 port.WorkflowStore，宿主在装配点
// 通过 AdaptWorkflowStore 注入真实 data 实现，从而避开包导入环。
type workflowStoreAdapter struct {
	db *data.SQLiteManager
}

func (a *workflowStoreAdapter) WatchStocks() []port.WatchStock {
	if a == nil || a.db == nil || a.db.GetDB() == nil {
		return nil
	}
	ws := data.GetDefaultWatchStocks(a.db.GetDB())
	out := make([]port.WatchStock, len(ws))
	for i, w := range ws {
		out[i] = port.WatchStock{Code: w.Code, Name: w.Name, Market: w.Market}
	}
	return out
}

func (a *workflowStoreAdapter) FetchStockSnapshots(codes []string) ([]port.StockSnapshot, error) {
	snaps, err := data.FetchRealStockSnapshots(codes)
	if err != nil {
		return nil, err
	}
	out := make([]port.StockSnapshot, len(snaps))
	for i, s := range snaps {
		out[i] = port.StockSnapshot{
			Code:          s.Code,
			Name:          s.Name,
			Market:        s.Market,
			CurrentPrice:  s.CurrentPrice,
			PrevClose:     s.PrevClose,
			Open:          s.Open,
			High:          s.High,
			Low:           s.Low,
			Volume:        s.Volume,
			Turnover:      s.Turnover,
			ChangePercent: s.ChangePercent,
			ChangeAmount:  s.ChangeAmount,
			Timestamp:     s.Timestamp,
			IsMock:        s.IsMock,
		}
	}
	return out, nil
}

func (a *workflowStoreAdapter) ActiveMarketIndices() []port.MarketIndex {
	if a == nil || a.db == nil || a.db.GetDB() == nil {
		return nil
	}
	ids := data.GetActiveMarketIndices(a.db.GetDB())
	out := make([]port.MarketIndex, len(ids))
	for i, idx := range ids {
		out[i] = port.MarketIndex{Code: idx.Code, Market: idx.Market}
	}
	return out
}

func (a *workflowStoreAdapter) FetchIndexSnapshots(codes []string) ([]port.IndexSnapshot, error) {
	snaps, err := data.FetchRealIndexSnapshots(codes)
	if err != nil {
		return nil, err
	}
	out := make([]port.IndexSnapshot, len(snaps))
	for i, s := range snaps {
		out[i] = port.IndexSnapshot{Code: s.Code, Current: s.Current, Change: s.Change}
	}
	return out, nil
}

func (a *workflowStoreAdapter) ActivePortfolio() (port.Portfolio, bool) {
	if a == nil || a.db == nil || a.db.GetDB() == nil {
		return port.Portfolio{}, false
	}
	var p data.Portfolio
	if err := a.db.GetDB().Where("is_active = 1").Order("id ASC").First(&p).Error; err != nil {
		return port.Portfolio{}, false
	}
	return port.Portfolio{InitialCapital: p.InitialCapital, CurrentCapital: p.CurrentCapital}, true
}

func (a *workflowStoreAdapter) DictLoader() port.DictLoader {
	return dictLoaderAdapter{inner: data.GetDictLoader()}
}

// dictLoaderAdapter 将宿主 internal/data.DictLoader 适配为决策脑 port.DictLoader。
type dictLoaderAdapter struct {
	inner *data.DictLoader
}

func (a dictLoaderAdapter) GetIndustryByStock(code string) string {
	return a.inner.GetIndustryByStock(code)
}
func (a dictLoaderAdapter) GetStockName(code string) string { return a.inner.GetStockName(code) }
func (a dictLoaderAdapter) GetStockCode(name string) string { return a.inner.GetStockCode(name) }

// AdaptWorkflowStore 将宿主 internal/data 的数据访问适配为决策脑 port.WorkflowStore。
// 宿主（internal/harness 等）在装配点调用本函数，构造 DailyCycle 所需的数据注入。
func AdaptWorkflowStore(db *data.SQLiteManager) port.WorkflowStore {
	return &workflowStoreAdapter{db: db}
}
