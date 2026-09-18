package backtest

import (
	"math"
)

// MA 简单移动平均线
func MA(closes []float64, period int) []float64 {
	n := len(closes)
	result := make([]float64, n)
	if period <= 0 || n == 0 {
		return result
	}
	sum := 0.0
	for i := 0; i < n; i++ {
		sum += closes[i]
		if i >= period {
			sum -= closes[i-period]
		}
		if i >= period-1 {
			result[i] = sum / float64(period)
		}
	}
	return result
}

// EMA 指数移动平均线
func EMA(values []float64, period int) []float64 {
	n := len(values)
	if n == 0 || period <= 0 {
		return make([]float64, n)
	}
	k := 2.0 / float64(period+1)
	result := make([]float64, n)
	result[0] = values[0]
	for i := 1; i < n; i++ {
		result[i] = values[i]*k + result[i-1]*(1-k)
	}
	return result
}

// MACD 指标，返回 DIF, DEA, HIST
func MACD(closes []float64, fast, slow, signal int) ([]float64, []float64, []float64) {
	fastEMA := EMA(closes, fast)
	slowEMA := EMA(closes, slow)
	n := len(closes)
	dif := make([]float64, n)
	for i := 0; i < n; i++ {
		dif[i] = fastEMA[i] - slowEMA[i]
	}
	dea := EMA(dif, signal)
	hist := make([]float64, n)
	for i := 0; i < n; i++ {
		hist[i] = (dif[i] - dea[i]) * 2
	}
	return dif, dea, hist
}

// RSI 相对强弱指标
func RSI(closes []float64, period int) []float64 {
	n := len(closes)
	result := make([]float64, n)
	if n <= period {
		return result
	}

	gains := make([]float64, n)
	losses := make([]float64, n)
	for i := 1; i < n; i++ {
		diff := closes[i] - closes[i-1]
		if diff > 0 {
			gains[i] = diff
		} else {
			losses[i] = -diff
		}
	}

	avgGain := 0.0
	avgLoss := 0.0
	for i := 1; i <= period; i++ {
		avgGain += gains[i]
		avgLoss += losses[i]
	}
	avgGain /= float64(period)
	avgLoss /= float64(period)

	if avgLoss == 0 {
		result[period] = 100
	} else {
		rs := avgGain / avgLoss
		result[period] = 100.0 - 100.0/(1.0+rs)
	}

	for i := period + 1; i < n; i++ {
		avgGain = (avgGain*float64(period-1) + gains[i]) / float64(period)
		avgLoss = (avgLoss*float64(period-1) + losses[i]) / float64(period)
		if avgLoss == 0 {
			result[i] = 100
		} else {
			rs := avgGain / avgLoss
			result[i] = 100.0 - 100.0/(1.0+rs)
		}
	}
	return result
}

// KDJ 指标，返回 K, D, J
func KDJ(closes, highs, lows []float64, n, m1, m2 int) ([]float64, []float64, []float64) {
	nn := len(closes)
	k := make([]float64, nn)
	d := make([]float64, nn)
	j := make([]float64, nn)
	if nn <= n {
		return k, d, j
	}

	// 计算 RSV：预热期（首 n-1 根）无法形成完整窗口，默认中性 RSV=50，
	// 而非默认 0——否则 K/D 初期的递推会把 0 混入平滑，造成指标初期异常、信号失真。
	rsv := make([]float64, nn)
	for i := 0; i < n-1; i++ {
		rsv[i] = 50
	}
	for i := n - 1; i < nn; i++ {
		highest := -math.MaxFloat64
		lowest := math.MaxFloat64
		for j := i - n + 1; j <= i; j++ {
			if highs[j] > highest {
				highest = highs[j]
			}
			if lows[j] < lowest {
				lowest = lows[j]
			}
		}
		if highest > lowest {
			rsv[i] = (closes[i] - lowest) / (highest - lowest) * 100
		} else {
			rsv[i] = 50
		}
	}

	// 初始化 K, D
	k[0] = 50
	d[0] = 50
	for i := 1; i < nn; i++ {
		if i <= n {
			k[i] = (float64(m1-1)*k[i-1] + rsv[i]) / float64(m1)
			d[i] = (float64(m2-1)*d[i-1] + k[i]) / float64(m2)
		} else {
			k[i] = (float64(m1-1)*k[i-1] + rsv[i]) / float64(m1)
			d[i] = (float64(m2-1)*d[i-1] + k[i]) / float64(m2)
		}
		j[i] = 3*k[i] - 2*d[i]
	}
	return k, d, j
}

// BOLL 布林带，返回上轨, 中轨, 下轨
func BOLL(closes []float64, period int, k float64) ([]float64, []float64, []float64) {
	n := len(closes)
	upper := make([]float64, n)
	mid := make([]float64, n)
	lower := make([]float64, n)
	if n <= period {
		return upper, mid, lower
	}

	ma := MA(closes, period)
	for i := period - 1; i < n; i++ {
		mid[i] = ma[i]
		variance := 0.0
		for j := i - period + 1; j <= i; j++ {
			diff := closes[j] - ma[i]
			variance += diff * diff
		}
		stdDev := math.Sqrt(variance / float64(period))
		upper[i] = ma[i] + k*stdDev
		lower[i] = ma[i] - k*stdDev
	}
	return upper, mid, lower
}

// BIAS 乖离率，返回 BIAS6, BIAS12, BIAS24
func BIAS(closes []float64) ([]float64, []float64, []float64) {
	bias6 := make([]float64, len(closes))
	bias12 := make([]float64, len(closes))
	bias24 := make([]float64, len(closes))

	ma6 := MA(closes, 6)
	ma12 := MA(closes, 12)
	ma24 := MA(closes, 24)

	for i := range closes {
		if ma6[i] > 0 {
			bias6[i] = (closes[i] - ma6[i]) / ma6[i] * 100
		}
		if ma12[i] > 0 {
			bias12[i] = (closes[i] - ma12[i]) / ma12[i] * 100
		}
		if ma24[i] > 0 {
			bias24[i] = (closes[i] - ma24[i]) / ma24[i] * 100
		}
	}
	return bias6, bias12, bias24
}

// OBV 能量潮
func OBV(closes []float64, volumes []int64) []float64 {
	n := len(closes)
	result := make([]float64, n)
	if n == 0 {
		return result
	}
	result[0] = float64(volumes[0])
	for i := 1; i < n; i++ {
		if closes[i] > closes[i-1] {
			result[i] = result[i-1] + float64(volumes[i])
		} else if closes[i] < closes[i-1] {
			result[i] = result[i-1] - float64(volumes[i])
		} else {
			result[i] = result[i-1]
		}
	}
	return result
}

// MFI 资金流量指标
func MFI(closes, highs, lows []float64, volumes []int64, period int) []float64 {
	n := len(closes)
	result := make([]float64, n)
	if n <= period {
		return result
	}

	// 典型价格
	tp := make([]float64, n)
	rawMF := make([]float64, n)
	for i := 0; i < n; i++ {
		tp[i] = (highs[i] + lows[i] + closes[i]) / 3
		rawMF[i] = tp[i] * float64(volumes[i])
	}

	// 正负资金流
	positiveMF := make([]float64, n)
	negativeMF := make([]float64, n)
	for i := 1; i < n; i++ {
		if tp[i] > tp[i-1] {
			positiveMF[i] = rawMF[i]
		} else if tp[i] < tp[i-1] {
			negativeMF[i] = rawMF[i]
		}
	}

	for i := period; i < n; i++ {
		sumPos := 0.0
		sumNeg := 0.0
		for j := i - period + 1; j <= i; j++ {
			sumPos += positiveMF[j]
			sumNeg += negativeMF[j]
		}
		if sumNeg == 0 {
			result[i] = 100
		} else {
			mfiRatio := sumPos / sumNeg
			result[i] = 100.0 - 100.0/(1.0+mfiRatio)
		}
	}
	return result
}

// MTM 动量指标，返回 MTM, MTMMA
func MTM(closes []float64, period int) ([]float64, []float64) {
	n := len(closes)
	mtm := make([]float64, n)
	for i := period; i < n; i++ {
		mtm[i] = closes[i] - closes[i-period]
	}
	mtmMA := MA(mtm, period)
	return mtm, mtmMA
}

// DMI 趋向指标，返回 PDI, MDI, ADX, ADXR
func DMI(closes, highs, lows []float64, period int) ([]float64, []float64, []float64, []float64) {
	n := len(closes)
	pdi := make([]float64, n)
	mdi := make([]float64, n)
	adx := make([]float64, n)
	adxr := make([]float64, n)
	if n <= period*2 {
		return pdi, mdi, adx, adxr
	}

	// 计算 TR, DM+, DM-
	tr := make([]float64, n)
	dmPlus := make([]float64, n)
	dmMinus := make([]float64, n)

	for i := 1; i < n; i++ {
		tr[i] = math.Max(highs[i]-lows[i],
			math.Max(math.Abs(highs[i]-closes[i-1]), math.Abs(lows[i]-closes[i-1])))

		upMove := highs[i] - highs[i-1]
		downMove := lows[i-1] - lows[i]

		if upMove > downMove && upMove > 0 {
			dmPlus[i] = upMove
		} else {
			dmPlus[i] = 0
		}

		if downMove > upMove && downMove > 0 {
			dmMinus[i] = downMove
		} else {
			dmMinus[i] = 0
		}
	}

	// 平滑 TR, DM+, DM-
	atr := WilderSmooth(tr, period)
	smoothDmPlus := WilderSmooth(dmPlus, period)
	smoothDmMinus := WilderSmooth(dmMinus, period)

	for i := period; i < n; i++ {
		if atr[i] > 0 {
			pdi[i] = smoothDmPlus[i] / atr[i] * 100
			mdi[i] = smoothDmMinus[i] / atr[i] * 100
		}
	}

	// ADX
	dx := make([]float64, n)
	for i := 0; i < n; i++ {
		sum := pdi[i] + mdi[i]
		if sum > 0 {
			dx[i] = math.Abs(pdi[i]-mdi[i]) / sum * 100
		}
	}
	adx = WilderSmooth(dx, period)

	// ADXR
	for i := period; i < n; i++ {
		adxr[i] = (adx[i] + adx[i-period]) / 2
	}

	return pdi, mdi, adx, adxr
}

// WilderSmooth Wilder 平滑法（用于 ATR, ADX 等）
func WilderSmooth(values []float64, period int) []float64 {
	n := len(values)
	result := make([]float64, n)
	if n <= period {
		return result
	}

	sum := 0.0
	for i := 0; i < period; i++ {
		sum += values[i]
	}
	result[period-1] = sum

	for i := period; i < n; i++ {
		result[i] = (result[i-1]*float64(period-1) + values[i]) / float64(period)
	}
	return result
}

// CCI 商品通道指标
func CCI(closes, highs, lows []float64, period int) []float64 {
	n := len(closes)
	result := make([]float64, n)
	if n <= period {
		return result
	}

	// TP = (H + L + C) / 3
	tp := make([]float64, n)
	for i := 0; i < n; i++ {
		tp[i] = (highs[i] + lows[i] + closes[i]) / 3
	}

	tpMA := MA(tp, period)

	for i := period - 1; i < n; i++ {
		// 平均偏差
		meanDev := 0.0
		for j := i - period + 1; j <= i; j++ {
			meanDev += math.Abs(tp[j] - tpMA[i])
		}
		meanDev /= float64(period)

		if meanDev > 0.01 {
			result[i] = (tp[i] - tpMA[i]) / (0.015 * meanDev)
		}
	}
	return result
}

// TRIX 三重指数平滑指标
func TRIX(closes []float64, period int) ([]float64, []float64) {
	n := len(closes)
	result := make([]float64, n)
	if n <= period*3 {
		return result, make([]float64, n)
	}

	// 三次 EMA 平滑
	ema1 := EMA(closes, period)
	ema2 := EMA(ema1, period)
	ema3 := EMA(ema2, period)

	for i := 1; i < n; i++ {
		if ema3[i-1] != 0 {
			result[i] = (ema3[i] - ema3[i-1]) / ema3[i-1] * 100
		}
	}

	trma := MA(result, period)
	return result, trma
}

// TAQ 唐安奇通道（海龟交易法），返回 upper, middle, lower
func TAQ(highs, lows []float64, period int) ([]float64, []float64, []float64) {
	n := len(highs)
	upper := make([]float64, n)
	middle := make([]float64, n)
	lower := make([]float64, n)
	if n <= period {
		return upper, middle, lower
	}

	for i := period; i < n; i++ {
		highest := -math.MaxFloat64
		lowest := math.MaxFloat64
		for j := i - period; j < i; j++ {
			if highs[j] > highest {
				highest = highs[j]
			}
			if lows[j] < lowest {
				lowest = lows[j]
			}
		}
		upper[i] = highest
		lower[i] = lowest
		middle[i] = (highest + lowest) / 2
	}
	return upper, middle, lower
}

// ZHUOYAO 捉妖大师指标（周期可配置），返回 LONG, MID, SHORT, TREND
func ZHUOYAO(closes []float64, shortP, midP, longP, trendP int) ([]float64, []float64, []float64, []float64) {
	n := len(closes)
	longArr := make([]float64, n)
	midArr := make([]float64, n)
	shortArr := make([]float64, n)
	trendArr := make([]float64, n)

	// 起始索引取各周期最大值，避免 midP/longP 大于起点导致负索引 panic
	start := longP
	if midP > start {
		start = midP
	}
	if shortP > start {
		start = shortP
	}
	if n <= start {
		return longArr, midArr, shortArr, trendArr
	}

	for i := start; i < n; i++ {
		// 短期涨幅
		if closes[i-shortP] > 0 {
			shortArr[i] = (closes[i]/closes[i-shortP] - 1) * 100
		}
		// 中期涨幅
		if closes[i-midP] > 0 {
			midArr[i] = (closes[i]/closes[i-midP] - 1) * 100
		}
		// 长期涨幅
		if closes[i-longP] > 0 {
			longArr[i] = (closes[i]/closes[i-longP] - 1) * 100
		}
	}

	// TREND = EMA of MID（中期涨幅的 EMA）
	trendArr = EMA(midArr, trendP)

	return longArr, midArr, shortArr, trendArr
}

// Crossover 判断金叉（A 上穿 B），在 bar_index 位置
func Crossover(a, b []float64, i int) bool {
	if i <= 0 || i >= len(a) || i >= len(b) {
		return false
	}
	return a[i-1] <= b[i-1] && a[i] > b[i]
}

// Crossunder 判断死叉（A 下穿 B），在 bar_index 位置
func Crossunder(a, b []float64, i int) bool {
	if i <= 0 || i >= len(a) || i >= len(b) {
		return false
	}
	return a[i-1] >= b[i-1] && a[i] < b[i]
}

// ==================== 新增指标：ATR / WMA / HMA / Aroon / CMF / SuperTrend ====================
// 均为公开领域的标准指标公式（与 cinar/indicator 思路一致，但按我方 slice 接口自实现，
// 不引入 AGPL-3.0 依赖），供新策略使用，也便于后续因子/盘面复用。

// ATR 平均真实波幅（Wilder 平滑）：真实波幅 TR 的前 period 根平均后递推 WMA 平滑。
// 返回数组与 closes 对齐；[0, period] 区间因数据不足为 0（策略应在 i>=period 后取用）。
func ATR(closes, highs, lows []float64, period int) []float64 {
	n := len(closes)
	result := make([]float64, n)
	if period <= 0 || n <= period {
		return result
	}
	tr := make([]float64, n)
	tr[0] = highs[0] - lows[0]
	for i := 1; i < n; i++ {
		hl := highs[i] - lows[i]
		hc := math.Abs(highs[i] - closes[i-1])
		lc := math.Abs(lows[i] - closes[i-1])
		tr[i] = math.Max(hl, math.Max(hc, lc))
	}
	sum := 0.0
	for j := 1; j <= period; j++ {
		sum += tr[j]
	}
	result[period] = sum / float64(period)
	for i := period + 1; i < n; i++ {
		result[i] = (result[i-1]*float64(period-1) + tr[i]) / float64(period)
	}
	return result
}

// WMA 线性加权移动平均：最近一根权重最高（period），权重线性递减到 1。
func WMA(values []float64, period int) []float64 {
	n := len(values)
	result := make([]float64, n)
	if period <= 0 || n == 0 {
		return result
	}
	for i := 0; i < n; i++ {
		if i < period-1 {
			continue
		}
		var sum, weightSum float64
		for j := 0; j < period; j++ {
			w := float64(period - j)
			sum += values[i-j] * w
			weightSum += w
		}
		result[i] = sum / weightSum
	}
	return result
}

// HMA 赫尔移动平均（Hull Moving Average）：2×WMA(n/2) − WMA(n) 再用 √n 加权的 WMA 平滑，
// 相较普通均线滞后更小、对趋势拐点更灵敏。
func HMA(values []float64, period int) []float64 {
	if period <= 0 {
		return make([]float64, len(values))
	}
	half := period / 2
	if half < 1 {
		half = 1
	}
	sqrtPeriod := int(math.Round(math.Sqrt(float64(period))))
	if sqrtPeriod < 1 {
		sqrtPeriod = 1
	}
	wmaHalf := WMA(values, half)
	wmaFull := WMA(values, period)
	raw := make([]float64, len(values))
	for i := range values {
		raw[i] = 2*wmaHalf[i] - wmaFull[i]
	}
	return WMA(raw, sqrtPeriod)
}

// Aroon 阿隆指标：衡量最近 period 根内创出新高/新低的时间距离。
// aroonUp / aroonDown ∈ [0,100]；(period−距最高距)比例越高，趋势越强。
// [0, period) 区间因数据不足为 0。
func Aroon(highs, lows []float64, period int) ([]float64, []float64) {
	n := len(highs)
	up := make([]float64, n)
	down := make([]float64, n)
	if period <= 0 || n == 0 {
		return up, down
	}
	for i := 0; i < n; i++ {
		if i < period {
			continue
		}
		hiIdx, loIdx := i, i
		for j := i - period; j <= i; j++ {
			if highs[j] >= highs[hiIdx] {
				hiIdx = j
			}
			if lows[j] <= lows[loIdx] {
				loIdx = j
			}
		}
		up[i] = float64(period-(i-hiIdx)) / float64(period) * 100
		down[i] = float64(period-(i-loIdx)) / float64(period) * 100
	}
	return up, down
}

// CMF 蔡金资金流量指标（Chaikin Money Flow）：周期内 每根K线的
// 资金流量乘数 MFM(Money-Flow-Multiplier) × 成交量 之和 ÷ 周期成交量之和。
// 正值代表资金持续净流入，负值代表净流出。值为 ±1 之间，通常以 ±0.05 为强弱阈值。
func CMF(closes, highs, lows, volumes []float64, period int) []float64 {
	n := len(closes)
	result := make([]float64, n)
	if period <= 0 || n == 0 {
		return result
	}
	for i := 0; i < n; i++ {
		if i < period-1 {
			continue
		}
		var mfvSum, volSum float64
		for j := i - period + 1; j <= i; j++ {
			mfm := 0.0
			hl := highs[j] - lows[j]
			if hl > 0 {
				mfm = ((closes[j] - lows[j]) - (highs[j] - closes[j])) / hl
			}
			vol := volumes[j]
			mfvSum += mfm * vol
			volSum += vol
		}
		if volSum > 0 {
			result[i] = mfvSum / volSum
		}
	}
	return result
}

// SuperTrend 超级趋势指标：基于 ATR 的动态跟踪趋势通道。
// 返回 supertrend（当根跟踪线）与 trend（1=多头、-1=空头，仅在前一波形方向保持时延续）。
// 方向翻转即买卖信号触发点；[0, period) 区间因 ATR 未稳定为占位值，调用方应延后取用信号。
func SuperTrend(closes, highs, lows []float64, period int, multiplier float64) ([]float64, []int) {
	n := len(closes)
	stLine := make([]float64, n)
	trend := make([]int, n)
	if period <= 0 || multiplier <= 0 || n == 0 {
		return stLine, trend
	}
	atr := ATR(closes, highs, lows, period)
	finalUpper := make([]float64, n)
	finalLower := make([]float64, n)
	// 起始：以首根中线作为上下轨基准，默认先按多头处理
	finalUpper[0] = (highs[0] + lows[0]) / 2
	finalLower[0] = (highs[0] + lows[0]) / 2
	trend[0] = 1
	stLine[0] = finalLower[0]

	for i := 1; i < n; i++ {
		mid := (highs[i] + lows[i]) / 2
		basicUpper := mid + multiplier*atr[i]
		basicLower := mid - multiplier*atr[i]

		// 上轨收敛：本根基本上轨低于上根上轨 或 收盘已突破上根上轨 → 采用本根基本上轨
		if basicUpper < finalUpper[i-1] || closes[i-1] > finalUpper[i-1] {
			finalUpper[i] = basicUpper
		} else {
			finalUpper[i] = finalUpper[i-1]
		}
		// 下轨收敛：本根基本下轨高于上根下轨 或 收盘已跌破上根下轨 → 采用本根基本下轨
		if basicLower > finalLower[i-1] || closes[i-1] < finalLower[i-1] {
			finalLower[i] = basicLower
		} else {
			finalLower[i] = finalLower[i-1]
		}

		if trend[i-1] == 1 {
			stLine[i] = finalLower[i]
			if closes[i] < finalLower[i] {
				trend[i] = -1
			} else {
				trend[i] = 1
			}
		} else {
			stLine[i] = finalUpper[i]
			if closes[i] > finalUpper[i] {
				trend[i] = 1
			} else {
				trend[i] = -1
			}
		}
	}
	return stLine, trend
}
