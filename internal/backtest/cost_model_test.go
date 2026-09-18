package backtest

import (
	"math"
	"testing"
)

// TestCostModelDefaults 默认 A 股成本模型参数
func TestCostModelDefaults(t *testing.T) {
	m := DefaultCostModel()
	if m.CommissionRate != 0.0003 {
		t.Errorf("CommissionRate = %v, want 0.0003", m.CommissionRate)
	}
	if m.MinCommission != 5 {
		t.Errorf("MinCommission = %v, want 5", m.MinCommission)
	}
	if m.StampDutyRate != 0.0005 {
		t.Errorf("StampDutyRate = %v, want 0.0005", m.StampDutyRate)
	}
	if m.ImpactCoeff != 1.0 {
		t.Errorf("ImpactCoeff = %v, want 1.0", m.ImpactCoeff)
	}
	if m.ImpactExponent != 1.5 {
		t.Errorf("ImpactExponent = %v, want 1.5", m.ImpactExponent)
	}
}

// TestBuyCostNoStamp 买入不收印花税，佣金有最低5元兜底
func TestBuyCostNoStamp(t *testing.T) {
	m := DefaultCostModel()
	// 小单：佣金低于最低5元 → 取5
	bd := m.BuyCost(1000, 1e8, 0.02) // 成交额1000元
	if bd.StampDuty != 0 {
		t.Errorf("买入不应收印花税, got %v", bd.StampDuty)
	}
	if bd.Commission != 5 {
		t.Errorf("小单佣金应取最低5元, got %v", bd.Commission)
	}
	// 大单：佣金按比例 0.03%
	bd2 := m.BuyCost(100000, 1e8, 0.02)
	if math.Abs(bd2.Commission-30) > 1e-6 {
		t.Errorf("大单佣金应=30元, got %v", bd2.Commission)
	}
}

// TestSellCostStamp 卖出收佣金+印花税
func TestSellCostStamp(t *testing.T) {
	m := DefaultCostModel()
	bd := m.SellCost(100000, 1e8, 0.02)
	if math.Abs(bd.Commission-30) > 1e-6 {
		t.Errorf("佣金应=30元, got %v", bd.Commission)
	}
	if math.Abs(bd.StampDuty-50) > 1e-6 {
		t.Errorf("印花税应=50元(0.05%%), got %v", bd.StampDuty)
	}
}

// TestImpactSquareRootLaw 冲击项满足平方根律：成交额翻8倍 → 冲击翻 8^1.5 倍
func TestImpactSquareRootLaw(t *testing.T) {
	m := DefaultCostModel()
	vol := 1e8
	sigma := 0.02
	x1 := 10000.0
	x2 := 80000.0
	i1 := m.impact(x1, vol, sigma)
	i2 := m.impact(x2, vol, sigma)
	if i1 <= 0 || i2 <= 0 {
		t.Fatalf("冲击项应为正, i1=%v i2=%v", i1, i2)
	}
	ratio := i2 / i1
	want := math.Pow(x2/x1, 1.5) // 8^1.5
	if math.Abs(ratio-want) > 1e-9 {
		t.Errorf("冲击比例 = %v, want %v", ratio, want)
	}
}

// TestImpactDisabled 关闭冲击系数后冲击项为0
func TestImpactDisabled(t *testing.T) {
	m := DefaultCostModel()
	m.ImpactCoeff = 0
	if i := m.impact(10000, 1e8, 0.02); i != 0 {
		t.Errorf("ImpactCoeff=0 时冲击应为0, got %v", i)
	}
}

// TestDailyVolNoLookahead 波动率序列第0根为0（无前视），且后续为trailing估计
func TestDailyVolNoLookahead(t *testing.T) {
	e := &BacktestEngine{
		Bars: []BarData{
			{Close: 10, Volume: 1000}, {Close: 10.5, Volume: 1000}, {Close: 9.5, Volume: 1000},
			{Close: 10.2, Volume: 1000}, {Close: 10.8, Volume: 1000}, {Close: 11.0, Volume: 1000},
		},
		CostModel: DefaultCostModel(),
	}
	e.precomputeVolSeries()
	if e.dailyVol(0) != 0 {
		t.Errorf("第0根不应有波动率估计（无前视）, got %v", e.dailyVol(0))
	}
	if e.dailyVol(1) != 0 {
		t.Errorf("第1根仅1个样本，不应有估计, got %v", e.dailyVol(1))
	}
	if e.dailyVol(5) <= 0 {
		t.Errorf("第5根应有波动率估计, got %v", e.dailyVol(5))
	}
}
