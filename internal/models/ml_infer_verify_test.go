package models

// 临时验证：对比 Go 推理 vs Python 预测（一次性，验证后删除）
// 依赖 verify/ 目录（由 verify_prep.py 生成）

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"testing"
)

type verifyCompare struct {
	Features    [][]float64            `json:"features"`
	PythonProba map[string][][]float64 `json:"python_proba"`
	PythonClass map[string][]int       `json:"python_class"`
}

func TestPythonComparison(t *testing.T) {
	dir := `C:\Users\JokerZ\AppData\Local\Temp\trae-agent-toolhost\verify`
	var cmp verifyCompare
	raw, err := os.ReadFile(filepath.Join(dir, "compare.json"))
	if err != nil {
		t.Fatalf("read compare.json: %v", err)
	}
	if err := json.Unmarshal(raw, &cmp); err != nil {
		t.Fatalf("parse compare.json: %v", err)
	}

	names := []string{"xgboost", "lgbm", "rf", "logistic", "mlp"}
	for _, name := range names {
		m, err := LoadMLModelByName(dir, name)
		if err != nil {
			t.Fatalf("load %s: %v", name, err)
		}
		sc, err := LoadStandardScalerForModel(dir, name)
		if err != nil {
			t.Fatalf("load %s scaler: %v", name, err)
		}
		pyClass := cmp.PythonClass[name]
		pyProba := cmp.PythonProba[name]
		maxClassErr := 0
		maxProbaErr := 0.0
		worst := -1
		for i, feat := range cmp.Features {
			scaled := sc.Transform(feat)
			gotClass := m.PredictClass(scaled)
			gotProba := m.PredictProba(scaled)
			if gotClass != pyClass[i] {
				maxClassErr++
				if worst < 0 {
					worst = i
				}
			}
			for k := 0; k < 3; k++ {
				d := math.Abs(gotProba[k] - pyProba[i][k])
				if d > maxProbaErr {
					maxProbaErr = d
				}
			}
		}
		if maxClassErr > 0 {
			t.Errorf("[%s] 类别不一致 %d/20 个（首个在样本#%d）", name, maxClassErr, worst)
		}
		if maxProbaErr > 1e-6 {
			t.Errorf("[%s] 概率最大偏差 %.6f > 1e-6", name, maxProbaErr)
		}
		t.Logf("[%s] OK 类别全对，概率最大偏差 %.8f", name, maxProbaErr)
	}
}
