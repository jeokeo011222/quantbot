// Command example 演示机器学习策略推理链路：
//
//	合成K线 → ComputeXGBFeatures(51维) → StandardScaler → 多模型集成预测（买入/观望/卖出）
//
// 运行方式：
//
//	cd go && go run ./example
//
// 若 go/models 下已有训练好的模型（由 python/train_from_duckdb.py 输出），
// 程序会加载并给出预测；否则提示先训练。
package main

import (
	"fmt"
	"math"
	"os"
	"path/filepath"

	"github.com/jeokeo011222/quantbot/opensource/ml-strategy/go/models"
)

// syntheticOHLC 生成一段合成K线（无真实行情依赖，仅演示链路）
func syntheticOHLC(n int) models.XGBOHLCV {
	var o models.XGBOHLCV
	o.Open = make([]float64, n)
	o.High = make([]float64, n)
	o.Low = make([]float64, n)
	o.Close = make([]float64, n)
	o.Volume = make([]float64, n)
	o.Amount = make([]float64, n)
	close := 10.0
	for i := 0; i < n; i++ {
		if i > 0 {
			close += math.Sin(float64(i)/7.0) * 0.1
		}
		o.Open[i] = close * (1 + math.Sin(float64(i)*0.3)*0.003)
		o.High[i] = math.Max(o.Open[i], close) * 1.01
		o.Low[i] = math.Min(o.Open[i], close) * 0.99
		o.Close[i] = close
		o.Volume[i] = 2e6 + float64(i)*1000
		o.Amount[i] = o.Volume[i] * close
	}
	return o
}

// findModelDir 依次查找模型目录（当前目录、仓库根 models/、go/models/）
func findModelDir() string {
	for _, cand := range []string{"models", "../models", "go/models"} {
		if fi, err := os.Stat(cand); err == nil && fi.IsDir() {
			return cand
		}
	}
	return ""
}

func main() {
	ohlc := syntheticOHLC(120)
	feats := models.ComputeXGBFeatures(ohlc)
	fmt.Printf("特征矩阵: %d 根K线 × %d 维\n", len(feats), models.XGBFeatureCount)

	dir := findModelDir()
	if dir == "" {
		fmt.Println("\n[提示] 未找到训练好的模型目录。")
		fmt.Println("  1. 准备 A 股日线数据：DuckDB 中建表 ohlc(symbol, date, open, high, low, close, volume, amount)")
		fmt.Println("  2. 训练：python python/train_from_duckdb.py --models xgboost,lgbm,rf,logistic,mlp")
		fmt.Println("  3. 将输出目录拷贝为 go/models/ 后重跑本示例")
		os.Exit(0)
	}

	names := []string{"astock_xgb_v1", "astock_lgbm_v1", "astock_rf_v1", "astock_logistic_v1", "astock_mlp_v1"}
	labels := []string{"卖出", "观望", "买入"}
	for _, name := range names {
		infoPath := filepath.Join(dir, name+"_info.json")
		if _, err := os.Stat(infoPath); err != nil {
			fmt.Printf("模型 %s 未生成，跳过\n", name)
			continue
		}
		m, err := models.LoadMLModelByName(dir, name)
		if err != nil {
			fmt.Printf("加载 %s 失败: %v\n", name, err)
			continue
		}
		sc, err := models.LoadStandardScalerForModel(dir, name)
		if err != nil {
			fmt.Printf("加载 %s scaler 失败: %v\n", name, err)
			continue
		}
		last := sc.Transform(feats[len(feats)-1])
		cls := m.PredictClass(last)
		proba := m.PredictProba(last)
		fmt.Printf("%-18s → 信号=%s  概率=[卖出 %.3f | 观望 %.3f | 买入 %.3f]\n",
			name, labels[cls], proba[0], proba[1], proba[2])
	}
}
