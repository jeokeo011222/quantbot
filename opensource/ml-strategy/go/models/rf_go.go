package models

// 纯 Go 实现的随机森林（sklearn RandomForestClassifier）推理。
// 解析 python/train_from_duckdb.py 导出的自定义 JSON：
//   - trees[i] 对应 estimators_[i].tree_
//   - children_left/children_right/feature/threshold 为节点数组（叶子 feature=-2）
//   - values[node] 为该叶子的各类别计数（原始计数，推理时归一化）
//   - sklearn 分裂规则：x[f] <= threshold 走 left，否则 right；缺失默认走 left
// 预测 = 各树叶概率平均，argmax 得类别。

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
)

// rfTree 单棵决策树
type rfTree struct {
	ChildrenLeft  []int       `json:"children_left"`
	ChildrenRight []int       `json:"children_right"`
	Feature       []int       `json:"feature"`
	Threshold     []float64   `json:"threshold"`
	Values        [][]float64 `json:"values"` // (n_nodes, n_classes) 原始计数
}

// rfDoc 随机森林 JSON 顶层结构
type rfDoc struct {
	NClasses    int      `json:"n_classes"`
	NEstimators int      `json:"n_estimators"`
	Trees       []rfTree `json:"trees"`
}

// RFModel 已加载的随机森林模型
type RFModel struct {
	nClasses int
	trees    []rfTree
}

// LoadRFModel 从随机森林 JSON 加载模型
func LoadRFModel(path string) (*RFModel, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读取随机森林模型失败: %w", err)
	}
	var doc rfDoc
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("解析随机森林模型JSON失败: %w", err)
	}
	if doc.NClasses <= 0 || len(doc.Trees) == 0 {
		return nil, fmt.Errorf("随机森林模型 n_classes/trees 无效")
	}
	return &RFModel{nClasses: doc.NClasses, trees: doc.Trees}, nil
}

// NumClass 返回类别数
func (m *RFModel) NumClass() int { return m.nClasses }

// predictTreeProb 单棵树叶概率
func (m *RFModel) predictTreeProb(t *rfTree, feats []float64) []float64 {
	node := 0
	for node >= 0 && node < len(t.Feature) && t.Feature[node] != -2 {
		f := t.Feature[node]
		var v float64
		isNaN := true
		if f >= 0 && f < len(feats) {
			v = feats[f]
			isNaN = math.IsNaN(v)
		}
		if isNaN {
			// sklearn 缺失默认走 left
			node = t.ChildrenLeft[node]
		} else if v <= t.Threshold[node] {
			node = t.ChildrenLeft[node]
		} else {
			node = t.ChildrenRight[node]
		}
	}
	probs := make([]float64, m.nClasses)
	if node >= 0 && node < len(t.Values) {
		sum := 0.0
		for k := 0; k < m.nClasses && k < len(t.Values[node]); k++ {
			probs[k] = t.Values[node][k]
			sum += probs[k]
		}
		if sum > 0 {
			for k := range probs {
				probs[k] /= sum
			}
		}
	}
	return probs
}

// PredictProba 各树叶概率平均
func (m *RFModel) PredictProba(feats []float64) []float64 {
	probs := make([]float64, m.nClasses)
	for i := range m.trees {
		tp := m.predictTreeProb(&m.trees[i], feats)
		for k := 0; k < m.nClasses; k++ {
			probs[k] += tp[k]
		}
	}
	if len(m.trees) > 0 {
		for k := range probs {
			probs[k] /= float64(len(m.trees))
		}
	}
	return probs
}

// PredictClass argmax 类别索引
func (m *RFModel) PredictClass(feats []float64) int {
	return argmaxIndex(m.PredictProba(feats))
}
