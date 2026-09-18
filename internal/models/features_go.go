package models

import "math"

// ---- 数学工具别名（保持代码可读） ----
func mathNaN() float64           { return math.NaN() }
func mathIsNaN(v float64) bool   { return math.IsNaN(v) }
func mathIsInf(v float64) bool   { return math.IsInf(v, 0) }
func mathSqrt(v float64) float64 { return math.Sqrt(v) }
func mathMax(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}
func mathMin(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}
func mathInf() float64 { return math.Inf(1) }

// 机器学习模型特征工程（纯 Go，与 python/ml_service.py FeatureEngineer 逐字段对齐）。
// 模型经 StandardScaler 缩放训练，特征顺序必须与 xgboost_astock_v1_info.json
// feature_columns 完全一致（共 51 维），否则推理结果会错乱。
//
// pandas 语义对齐要点：
//   - rolling(n).mean()/std()/min()/max()：窗口前 n-1 行为 NaN
//   - rolling(n).std() 使用 ddof=1（样本标准差）
//   - ewm(span=.., adjust=False)：alpha=2/(span+1) 的递归EMA
//   - ewm(com=.., adjust=False)：alpha=1/(1+com) 的递归EMA
//   - pct_change(n)：(x[i]-x[i-n])/x[i-n]
//   - 分母一律加 1e-10 与 Python 端保持一致
// 最终所有 NaN 填 0（对应 Python 的 .fillna(0)）。

// xgbFeatureCols 51 个特征名（顺序即模型特征列顺序）
var xgbFeatureCols = []string{
	"open", "high", "low", "close", "volume", "amount",
	"return_1d", "return_5d", "return_10d", "return_20d",
	"volatility_5", "volatility_10", "volatility_20",
	"ma_5", "ma_ratio_5", "ma_10", "ma_ratio_10", "ma_20", "ma_ratio_20", "ma_60", "ma_ratio_60",
	"ma_alignment",
	"rsi_6", "rsi_14",
	"macd_dif", "macd_dea", "macd_hist", "macd_cross",
	"kdj_k", "kdj_d", "kdj_j",
	"boll_mid", "boll_std", "boll_upper", "boll_lower", "boll_position",
	"vol_ma_5", "vol_ma_10", "vol_ma_20", "vol_ratio", "vol_surge",
	"body_ratio", "upper_shadow", "lower_shadow", "is_yang",
	"momentum_10", "momentum_20",
	"high_20d", "low_20d", "new_high", "new_low",
}

// XGBFeatureCount 特征维度
const XGBFeatureCount = 51

// XGBOHLCV 输入K线序列（长度一致）
type XGBOHLCV struct {
	Open   []float64
	High   []float64
	Low    []float64
	Close  []float64
	Volume []float64
	Amount []float64
}

// ComputeXGBFeatures 计算全部样本的 51 维特征（已做 fillna(0)）。
func ComputeXGBFeatures(ohlc XGBOHLCV) [][]float64 {
	n := len(ohlc.Close)
	if n == 0 {
		return nil
	}
	f := &xgbFeatures{n: n, ohlc: ohlc}

	// 基础价格/成交序列（原始特征直接参与）
	open, high, low, close := ohlc.Open, ohlc.High, ohlc.Low, ohlc.Close
	volume, amount := ohlc.Volume, ohlc.Amount

	// 收益
	r1 := pctChange(close, 1)
	r5 := pctChange(close, 5)
	r10 := pctChange(close, 10)
	r20 := pctChange(close, 20)
	// 波动率
	vol5 := rollingStd(r1, 5)
	vol10 := rollingStd(r1, 10)
	vol20 := rollingStd(r1, 20)
	// 均线
	ma5 := rollingMean(close, 5)
	ma10 := rollingMean(close, 10)
	ma20 := rollingMean(close, 20)
	ma60 := rollingMean(close, 60)
	maR5 := ratioTo(close, ma5)
	maR10 := ratioTo(close, ma10)
	maR20 := ratioTo(close, ma20)
	maR60 := ratioTo(close, ma60)
	// 均线排列
	align := make([]float64, n)
	for i := 0; i < n; i++ {
		a := 0.0
		if ma5[i] > ma10[i] {
			a++
		}
		if ma10[i] > ma20[i] {
			a++
		}
		if ma20[i] > ma60[i] {
			a++
		}
		align[i] = a
	}
	// RSI
	rsi6 := calcRSI(close, 6)
	rsi14 := calcRSI(close, 14)
	// MACD
	macdDif, macdDea, macdHist, macdCross := calcMACD(close)
	// KDJ
	kdjK, kdjD, kdjJ := calcKDJ(high, low, close)
	// 布林带
	bollMid := rollingMean(close, 20)
	bollStd := rollingStd(close, 20)
	bollUp := make([]float64, n)
	bollLo := make([]float64, n)
	bollPos := make([]float64, n)
	for i := 0; i < n; i++ {
		bollUp[i] = bollMid[i] + 2*bollStd[i]
		bollLo[i] = bollMid[i] - 2*bollStd[i]
		bollPos[i] = (close[i] - bollLo[i]) / (bollUp[i] - bollLo[i] + 1e-10)
	}
	// 成交量
	volMA5 := rollingMean(volume, 5)
	volMA10 := rollingMean(volume, 10)
	volMA20 := rollingMean(volume, 20)
	volRatio := make([]float64, n)
	volSurge := make([]float64, n)
	for i := 0; i < n; i++ {
		volRatio[i] = volume[i] / volMA20[i]
		if volume[i] > 2*volMA20[i] {
			volSurge[i] = 1
		}
	}
	// 价格形态
	bodyRatio := make([]float64, n)
	upperShadow := make([]float64, n)
	lowerShadow := make([]float64, n)
	isYang := make([]float64, n)
	for i := 0; i < n; i++ {
		rng := high[i] - low[i] + 1e-10
		bodyRatio[i] = (close[i] - open[i]) / rng
		upperShadow[i] = (high[i] - mathMax(open[i], close[i])) / rng
		lowerShadow[i] = (mathMin(open[i], close[i]) - low[i]) / rng
		if close[i] > open[i] {
			isYang[i] = 1
		}
	}
	// 动量
	mom10 := pctChange(close, 10)
	mom20 := pctChange(close, 20)
	// 新高/新低
	high20 := rollingMax(high, 20)
	low20 := rollingMin(low, 20)
	newHigh := make([]float64, n)
	newLow := make([]float64, n)
	for i := 0; i < n; i++ {
		if close[i] >= high20[i] {
			newHigh[i] = 1
		}
		if close[i] <= low20[i] {
			newLow[i] = 1
		}
	}

	// 组装 51 维（顺序与 xgbFeatureCols 一致）
	cols := [][]float64{
		open, high, low, close, volume, amount,
		r1, r5, r10, r20,
		vol5, vol10, vol20,
		ma5, maR5, ma10, maR10, ma20, maR20, ma60, maR60,
		align,
		rsi6, rsi14,
		macdDif, macdDea, macdHist, macdCross,
		kdjK, kdjD, kdjJ,
		bollMid, bollStd, bollUp, bollLo, bollPos,
		volMA5, volMA10, volMA20, volRatio, volSurge,
		bodyRatio, upperShadow, lowerShadow, isYang,
		mom10, mom20,
		high20, low20, newHigh, newLow,
	}
	_ = f

	out := make([][]float64, n)
	for i := 0; i < n; i++ {
		row := make([]float64, XGBFeatureCount)
		for c := 0; c < XGBFeatureCount; c++ {
			v := cols[c][i]
			if mathIsNaN(v) || mathIsInf(v) {
				v = 0 // 对应 Python .fillna(0)
			}
			row[c] = v
		}
		out[i] = row
	}
	return out
}

type xgbFeatures struct {
	n    int
	ohlc XGBOHLCV
}

// ---- 基础统计工具（pandas 语义） ----

func pctChange(x []float64, lag int) []float64 {
	n := len(x)
	out := make([]float64, n)
	for i := 0; i < n; i++ {
		if i >= lag && x[i-lag] != 0 {
			out[i] = x[i]/x[i-lag] - 1
		} else {
			out[i] = mathNaN()
		}
	}
	return out
}

func ratioTo(x, base []float64) []float64 {
	n := len(x)
	out := make([]float64, n)
	for i := 0; i < n; i++ {
		if base[i] != 0 && !mathIsNaN(base[i]) {
			out[i] = x[i]/base[i] - 1
		} else {
			out[i] = mathNaN()
		}
	}
	return out
}

// rollingMean 滚动均值（pandas 默认 min_periods=window：窗口内任一 NaN 则结果 NaN）
func rollingMean(x []float64, w int) []float64 {
	n := len(x)
	out := make([]float64, n)
	for i := 0; i < n; i++ {
		if i < w-1 {
			out[i] = mathNaN()
			continue
		}
		s := 0.0
		ok := true
		for j := i - w + 1; j <= i; j++ {
			if mathIsNaN(x[j]) {
				ok = false
				break
			}
			s += x[j]
		}
		if !ok {
			out[i] = mathNaN()
		} else {
			out[i] = s / float64(w)
		}
	}
	return out
}

// rollingStd 滚动标准差（ddof=1，与 pandas rolling().std() 一致；窗口内任一 NaN 则结果 NaN）
func rollingStd(x []float64, w int) []float64 {
	n := len(x)
	out := make([]float64, n)
	for i := 0; i < n; i++ {
		if i < w-1 {
			out[i] = mathNaN()
			continue
		}
		ok := true
		for j := i - w + 1; j <= i; j++ {
			if mathIsNaN(x[j]) {
				ok = false
				break
			}
		}
		if !ok {
			out[i] = mathNaN()
			continue
		}
		mean := 0.0
		for j := i - w + 1; j <= i; j++ {
			mean += x[j]
		}
		mean /= float64(w)
		s := 0.0
		for j := i - w + 1; j <= i; j++ {
			d := x[j] - mean
			s += d * d
		}
		out[i] = mathSqrt(s / float64(w-1)) // ddof=1
	}
	return out
}

func rollingMin(x []float64, w int) []float64 {
	n := len(x)
	out := make([]float64, n)
	for i := 0; i < n; i++ {
		if i < w-1 {
			out[i] = mathNaN()
			continue
		}
		ok := true
		m := mathInf()
		for j := i - w + 1; j <= i; j++ {
			if mathIsNaN(x[j]) {
				ok = false
				break
			}
			if x[j] < m {
				m = x[j]
			}
		}
		if !ok {
			out[i] = mathNaN()
		} else {
			out[i] = m
		}
	}
	return out
}

func rollingMax(x []float64, w int) []float64 {
	n := len(x)
	out := make([]float64, n)
	for i := 0; i < n; i++ {
		if i < w-1 {
			out[i] = mathNaN()
			continue
		}
		ok := true
		m := -mathInf()
		for j := i - w + 1; j <= i; j++ {
			if mathIsNaN(x[j]) {
				ok = false
				break
			}
			if x[j] > m {
				m = x[j]
			}
		}
		if !ok {
			out[i] = mathNaN()
		} else {
			out[i] = m
		}
	}
	return out
}

// ewmMean ewm(span=.., adjust=False) 递归EMA
func ewmMean(x []float64, alpha float64) []float64 {
	n := len(x)
	out := make([]float64, n)
	if n == 0 {
		return out
	}
	prev := mathNaN()
	for i := 0; i < n; i++ {
		if mathIsNaN(x[i]) {
			out[i] = mathNaN()
			continue
		}
		if mathIsNaN(prev) {
			prev = x[i]
		} else {
			prev = (1-alpha)*prev + alpha*x[i]
		}
		out[i] = prev
	}
	return out
}

// calcRSI 与 FeatureEngineer._calculate_rsi 一致（滚动均值增益/损失）
func calcRSI(prices []float64, period int) []float64 {
	n := len(prices)
	delta := make([]float64, n)
	for i := 1; i < n; i++ {
		delta[i] = prices[i] - prices[i-1]
	}
	// 与 pandas delta.where(delta>0,0) 一致：delta[0]=NaN 时也置 0
	gain := make([]float64, n)
	loss := make([]float64, n)
	for i := 1; i < n; i++ {
		if delta[i] > 0 {
			gain[i] = delta[i]
		} else if delta[i] < 0 {
			loss[i] = -delta[i]
		}
	}
	avgGain := rollingMean(gain, period)
	avgLoss := rollingMean(loss, period)
	out := make([]float64, n)
	for i := 0; i < n; i++ {
		if mathIsNaN(avgGain[i]) || mathIsNaN(avgLoss[i]) {
			out[i] = mathNaN()
			continue
		}
		rs := avgGain[i] / (avgLoss[i] + 1e-10)
		out[i] = 100 - 100/(1+rs)
	}
	return out
}

// calcMACD ewm(span=12/26, adjust=False) 再 ewm(span=9)
func calcMACD(close []float64) (dif, dea, hist, cross []float64) {
	n := len(close)
	ema12 := ewmMean(close, 2.0/13.0) // span=12 -> alpha=2/13
	ema26 := ewmMean(close, 2.0/27.0) // span=26 -> alpha=2/27
	dif = make([]float64, n)
	for i := 0; i < n; i++ {
		if mathIsNaN(ema12[i]) || mathIsNaN(ema26[i]) {
			dif[i] = mathNaN()
		} else {
			dif[i] = ema12[i] - ema26[i]
		}
	}
	dea = ewmMean(dif, 2.0/10.0) // span=9 -> alpha=2/10
	hist = make([]float64, n)
	cross = make([]float64, n)
	for i := 0; i < n; i++ {
		if mathIsNaN(dif[i]) || mathIsNaN(dea[i]) {
			hist[i] = mathNaN()
			cross[i] = mathNaN()
			continue
		}
		hist[i] = 2 * (dif[i] - dea[i])
		var prevD, prevE float64
		hasPrev := i > 0 && !mathIsNaN(dif[i-1]) && !mathIsNaN(dea[i-1])
		if hasPrev {
			prevD, prevE = dif[i-1], dea[i-1]
		}
		if hasPrev && dif[i] > dea[i] && prevD <= prevE {
			cross[i] = 1
		} else if hasPrev && dif[i] < dea[i] && prevD >= prevE {
			cross[i] = -1
		} else {
			cross[i] = 0
		}
	}
	return dif, dea, hist, cross
}

// calcKDJ 与 FeatureEngineer KDJ 一致（rolling9 min/max + ewm(com=2)）
func calcKDJ(high, low, close []float64) (k, d, j []float64) {
	n := len(close)
	lowMin := rollingMin(low, 9)
	highMax := rollingMax(high, 9)
	rsv := make([]float64, n)
	for i := 0; i < n; i++ {
		if mathIsNaN(lowMin[i]) || mathIsNaN(highMax[i]) {
			rsv[i] = mathNaN()
		} else {
			rsv[i] = (close[i] - lowMin[i]) / (highMax[i] - lowMin[i] + 1e-10) * 100
		}
	}
	k = ewmMean(rsv, 1.0/3.0) // com=2 -> alpha=1/3
	d = ewmMean(k, 1.0/3.0)
	j = make([]float64, n)
	for i := 0; i < n; i++ {
		if mathIsNaN(k[i]) || mathIsNaN(d[i]) {
			j[i] = mathNaN()
		} else {
			j[i] = 3*k[i] - 2*d[i]
		}
	}
	return k, d, j
}
