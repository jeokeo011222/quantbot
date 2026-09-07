package models

// 冒烟测试：加载正式生产模型（astock_*_v1）并用合成K线做推理，验证 Go 推理器可用
// 依赖 build/bin/models 下已训练好的模型文件；缺失的模型跳过不报错。

import (
	"math"
	"os"
	"path/filepath"
	"testing"
)

func syntheticOHLC(n int) XGBOHLCV {
	var o XGBOHLCV
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

func TestProductionModelsSmoke(t *testing.T) {
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	// 模型目录查找：包内 models/ 或仓库根 models/（训练脚本默认输出到仓库根 models/）
	var modelDir string
	for _, cand := range []string{
		filepath.Join(dir, "models"),
		filepath.Join(dir, "..", "..", "models"),
		filepath.Join(dir, "testdata"),
	} {
		if fi, err := os.Stat(cand); err == nil && fi.IsDir() {
			modelDir = cand
			break
		}
	}
	if modelDir == "" {
		t.Logf("未找到模型目录，跳过冒烟测试")
		return
	}
	t.Logf("model dir: %s", modelDir)

	names := []string{"astock_lgbm_v1", "astock_rf_v1", "astock_logistic_v1", "astock_mlp_v1"}
	ohlc := syntheticOHLC(120)
	feats := ComputeXGBFeatures(ohlc)
	if len(feats) != 120 {
		t.Fatalf("特征数错误: %d", len(feats))
	}
	loaded := 0
	for _, name := range names {
		infoPath := filepath.Join(modelDir, name+"_info.json")
		if _, err := os.Stat(infoPath); err != nil {
			t.Logf("模型 %s 未生成，跳过", name)
			continue
		}
		m, err := LoadMLModelByName(modelDir, name)
		if err != nil {
			t.Errorf("加载 %s 失败: %v", name, err)
			continue
		}
		sc, err := LoadStandardScalerForModel(modelDir, name)
		if err != nil {
			t.Errorf("加载 %s scaler 失败: %v", name, err)
			continue
		}
		last := sc.Transform(feats[len(feats)-1])
		cls := m.PredictClass(last)
		proba := m.PredictProba(last)
		if cls < 0 || cls > 2 {
			t.Errorf("%s 非法类别 %d", name, cls)
		}
		t.Logf("%s OK: class=%d proba=%v", name, cls, proba)
		loaded++
	}
	if loaded == 0 {
		t.Logf("没有任何生产模型可测（训练未完成？）")
		return
	}
	t.Logf("共验证 %d 个生产模型", loaded)
}
