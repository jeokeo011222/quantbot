package backtest

import (
	"math"
	"testing"
)

// MA：滑动窗口正确性与边界（不足窗口输出0）。
func TestMA(t *testing.T) {
	got := MA([]float64{1, 2, 3, 4, 5}, 3)
	// 索引2=(1+2+3)/3=2, 3=(2+3+4)/3=3, 4=(3+4+5)/3=4
	expect := []float64{0, 0, 2, 3, 4}
	for i := range expect {
		if math.Abs(got[i]-expect[i]) > 1e-9 {
			t.Fatalf("MA index %d = %.3f, expect %.3f", i, got[i], expect[i])
		}
	}
	if MA(nil, 3) == nil || len(MA([]float64{}, 3)) != 0 {
		t.Fatal("空输入应返回空切片")
	}
}

// EMA：周期3验证平滑递推。
func TestEMA(t *testing.T) {
	got := EMA([]float64{1, 2, 3}, 3) // k=2/4=0.5
	if math.Abs(got[0]-1) > 1e-9 {
		t.Fatalf("EMA[0]=%.4f expect 1", got[0])
	}
	if math.Abs(got[1]-(2*0.5+1*0.5)) > 1e-9 {
		t.Fatalf("EMA[1]=%.4f expect %.4f", got[1], 1.5)
	}
	if math.Abs(got[2]-(3*0.5+1.5*0.5)) > 1e-9 {
		t.Fatalf("EMA[2]=%.4f expect %.4f", got[2], 2.25)
	}
	if EMA(nil, 3) == nil {
		t.Fatal("EMA 应返回非nil切片")
	}
}

// RSI：单边上涨→100，单边下跌→0。
func TestRSI(t *testing.T) {
	up := make([]float64, 20)
	for i := range up {
		up[i] = float64(i + 1) // 单调上涨
	}
	r1 := RSI(up, 14)
	if math.Abs(r1[14]-100) > 1e-6 {
		t.Fatalf("单调上涨 RSI[14]=%.4f, expect 100", r1[14])
	}
	down := make([]float64, 20)
	for i := range down {
		down[i] = float64(20 - i) // 单调下跌
	}
	r2 := RSI(down, 14)
	if math.Abs(r2[14]-0) > 1e-6 {
		t.Fatalf("单调下跌 RSI[14]=%.4f, expect 0", r2[14])
	}
	r3 := RSI([]float64{1, 2, 3}, 14) // 样本过短
	for _, v := range r3 {
		if v != 0 {
			t.Fatalf("样本过短 RSI 应为0, got %.4f", v)
		}
	}
}

// MACD：恒定收盘价→DIF/DEA/HIST 全为0（EMA恒定，差值0）。
func TestMACDConstantSeries(t *testing.T) {
	closes := make([]float64, 40)
	for i := range closes {
		closes[i] = 10
	}
	dif, dea, hist := MACD(closes, 12, 26, 9)
	for i := range closes {
		if math.Abs(dif[i]) > 1e-9 || math.Abs(dea[i]) > 1e-9 || math.Abs(hist[i]) > 1e-9 {
			t.Fatalf("恒定收盘 MACD 应全0, i=%d dif=%.4f dea=%.4f hist=%.4f", i, dif[i], dea[i], hist[i])
		}
	}
}

// KDJ：价格无波动(high==low==close)→RSV=50→K/D=50, J=50。
func TestKDJFlat(t *testing.T) {
	closes := make([]float64, 30)
	highs := make([]float64, 30)
	lows := make([]float64, 30)
	for i := range closes {
		closes[i], highs[i], lows[i] = 10, 10, 10
	}
	k, d, j := KDJ(closes, highs, lows, 9, 3, 3)
	// 平价时 RSV=50，K/D/J 收敛到 50（前 n-1 根 K 线 RSV 未参与计算，故从 0 起步收敛，容差放宽）
	if math.Abs(k[29]-50) > 1.0 || math.Abs(d[29]-50) > 1.0 || math.Abs(j[29]-50) > 1.0 {
		t.Fatalf("平价KDJ应趋近50, k=%.4f d=%.4f j=%.4f", k[29], d[29], j[29])
	}
}

// BOLL：恒定收盘→标准差0→上/中/下轨相等且=收盘。
func TestBOLLFlat(t *testing.T) {
	closes := make([]float64, 30)
	for i := range closes {
		closes[i] = 10
	}
	upper, mid, lower := BOLL(closes, 20, 2)
	for i := 19; i < len(closes); i++ {
		if math.Abs(mid[i]-10) > 1e-9 || math.Abs(upper[i]-10) > 1e-9 || math.Abs(lower[i]-10) > 1e-9 {
			t.Fatalf("平价BOLL上/中/下轨应=10, i=%d u=%.3f m=%.3f l=%.3f", i, upper[i], mid[i], lower[i])
		}
	}
}

// Crossover/Crossunder 金叉死叉判定与越界保护。
func TestCrossover(t *testing.T) {
	if !Crossover([]float64{1, 2}, []float64{2, 1}, 1) {
		t.Fatal("A上穿B应判金叉")
	}
	if Crossover([]float64{2, 1}, []float64{1, 2}, 1) {
		t.Fatal("A下穿B不应判金叉")
	}
	if Crossover([]float64{1, 2}, []float64{1, 2}, 0) || Crossover([]float64{1, 2}, []float64{1, 2}, 5) {
		t.Fatal("越界索引应返回false")
	}
	if !Crossunder([]float64{2, 1}, []float64{1, 2}, 1) {
		t.Fatal("A下穿B应判死叉")
	}
	if Crossunder([]float64{1, 2}, []float64{2, 1}, 1) {
		t.Fatal("A上穿B不应判死叉")
	}
}

// WilderSmooth：首周期累加，后续递推。
func TestWilderSmooth(t *testing.T) {
	v := []float64{1, 2, 3, 4, 5, 6, 7}
	got := WilderSmooth(v, 3)
	if math.Abs(got[2]-6) > 1e-9 {
		t.Fatalf("Wilder首周期累加应=6, got %.4f", got[2])
	}
	// got[3] = (got[2]*2 + v[3])/3 = (12+4)/3 = 16/3
	if math.Abs(got[3]-16.0/3.0) > 1e-9 {
		t.Fatalf("Wilder递推 got[3]=%.6f expect %.6f", got[3], 16.0/3.0)
	}
}
