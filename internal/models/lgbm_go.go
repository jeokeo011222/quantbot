package models

// 纯 Go 实现的 LightGBM 模型推理（无 Python 依赖）。
// 解析 python/train_from_duckdb.py 通过 Booster.dump_model() 导出的 JSON：
//   - 顶层 num_class / tree_info 数组
//   - 每棵树 tree_structure 为嵌套 JSON（数值分裂 decision_type "<="，left 条件 x<=threshold）
//   - multiclass 下树按轮次主序排列：tree_info[t*numClass+k] 为第 k 类的第 t 棵树
//   - 类别原始分 = 该类别所有树叶值之和；softmax 得概率
//   - 叶节点 leaf_value 为已应用学习率/收缩后的最终值（dump 中每棵树 shrinkage=1）

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
)

// lgbmNode LightGBM 树节点（嵌套结构）
type lgbmNode struct {
	SplitFeature int       `json:"split_feature"`
	Threshold    float64   `json:"threshold"`
	DefaultLeft  bool      `json:"default_left"`
	LeafValue    *float64  `json:"leaf_value"`
	LeftChild    *lgbmNode `json:"left_child"`
	RightChild   *lgbmNode `json:"right_child"`
}

// lgbmTreeInfo 单棵树信息
type lgbmTreeInfo struct {
	TreeIndex     int       `json:"tree_index"`
	TreeStructure *lgbmNode `json:"tree_structure"`
}

// lgbmDoc LightGBM dump_model JSON 顶层结构
type lgbmDoc struct {
	NumClass            int            `json:"num_class"`
	NumTreePerIteration int            `json:"num_tree_per_iteration"`
	TreeInfo            []lgbmTreeInfo `json:"tree_info"`
}

// LGBMModel 已加载的 LightGBM 模型
type LGBMModel struct {
	numClass int
	trees    []*lgbmNode // 展平为 (iter, class) 顺序：trees[t*numClass+k]
}

// LoadLGBMModel 从 LightGBM dump_model JSON 加载模型
func LoadLGBMModel(path string) (*LGBMModel, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读取LightGBM模型失败: %w", err)
	}
	var doc lgbmDoc
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("解析LightGBM模型JSON失败: %w", err)
	}
	if doc.NumClass <= 0 {
		doc.NumClass = doc.NumTreePerIteration
	}
	if doc.NumClass <= 0 {
		return nil, fmt.Errorf("LightGBM模型 num_class 无效")
	}
	if len(doc.TreeInfo) == 0 {
		return nil, fmt.Errorf("LightGBM模型不含任何树")
	}
	m := &LGBMModel{numClass: doc.NumClass}
	for _, ti := range doc.TreeInfo {
		if ti.TreeStructure == nil {
			return nil, fmt.Errorf("LightGBM树 %d 无 tree_structure", ti.TreeIndex)
		}
		m.trees = append(m.trees, ti.TreeStructure)
	}
	return m, nil
}

// NumClass 返回类别数
func (m *LGBMModel) NumClass() int { return m.numClass }

// predictTree 单棵树推理，返回叶值
func (m *LGBMModel) predictTree(node *lgbmNode, feats []float64) float64 {
	for node != nil && node.LeafValue == nil {
		var v float64
		isNaN := true
		if node.SplitFeature >= 0 && node.SplitFeature < len(feats) {
			v = feats[node.SplitFeature]
			isNaN = math.IsNaN(v)
		}
		if isNaN {
			// 缺失值：按 default_left 决定走向
			if node.DefaultLeft {
				node = node.LeftChild
			} else {
				node = node.RightChild
			}
		} else if v <= node.Threshold {
			node = node.LeftChild
		} else {
			node = node.RightChild
		}
	}
	if node == nil || node.LeafValue == nil {
		return 0
	}
	return *node.LeafValue
}

// rawScores 计算各类别原始分
// 注意：dump_model 导出的 leaf_value 已应用学习率（shrinkage 已折算进叶值），
// 直接累加即可，不能再乘 tree_info.shrinkage（该字段仅为训练时的学习率元数据）。
func (m *LGBMModel) rawScores(feats []float64) []float64 {
	scores := make([]float64, m.numClass)
	iters := len(m.trees) / m.numClass
	for k := 0; k < m.numClass; k++ {
		s := 0.0
		for t := 0; t < iters; t++ {
			idx := t*m.numClass + k
			if idx < len(m.trees) && m.trees[idx] != nil {
				s += m.predictTree(m.trees[idx], feats)
			}
		}
		scores[k] = s
	}
	return scores
}

// PredictProba softmax 概率
func (m *LGBMModel) PredictProba(feats []float64) []float64 {
	scores := m.rawScores(feats)
	return softmax(scores)
}

// PredictClass argmax 类别索引
func (m *LGBMModel) PredictClass(feats []float64) int {
	scores := m.rawScores(feats)
	return argmaxIndex(scores)
}

// softmax 数值稳定 softmax
func softmax(scores []float64) []float64 {
	out := make([]float64, len(scores))
	if len(scores) == 0 {
		return out
	}
	maxV := scores[0]
	for _, s := range scores {
		if s > maxV {
			maxV = s
		}
	}
	sum := 0.0
	for i, s := range scores {
		out[i] = math.Exp(s - maxV)
		sum += out[i]
	}
	if sum > 0 {
		for i := range out {
			out[i] /= sum
		}
	}
	return out
}

// argmaxIndex 返回最大值的下标
func argmaxIndex(vals []float64) int {
	if len(vals) == 0 {
		return 0
	}
	best, idx := vals[0], 0
	for i := 1; i < len(vals); i++ {
		if vals[i] > best {
			best, idx = vals[i], i
		}
	}
	return idx
}
