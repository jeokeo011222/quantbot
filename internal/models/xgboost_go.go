package models

// 纯 Go 实现的 XGBoost 模型推理（无 Python 依赖）。
// 支持从 XGBoost save_model 导出的 JSON 模型文件加载并预测。
// 已针对本项目 multi:softmax 三分类模型验证：
//   - 树按类别交错排列：trees[t*numClass+k] 为第 k 类的第 t 棵树
//   - 叶节点值取 split_conditions[leaf]
//   - 类别 margin = base_score + 该类别所有树输出之和
//   - 预测类别 = argmax(margin)

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"
)

// xgTree XGBoost 单棵树（JSON 格式）
type xgTree struct {
	SplitConditions []float64 `json:"split_conditions"`
	SplitIndices    []int     `json:"split_indices"`
	LeftChildren    []int     `json:"left_children"`
	RightChildren   []int     `json:"right_children"`
	DefaultLeft     []int     `json:"default_left"`
}

// XGBoostModel 已加载的 XGBoost 模型
type XGBoostModel struct {
	trees         []xgTree
	numClass      int
	treesPerClass int
	baseScore     float64
}

// LoadXGBoostModel 从 XGBoost JSON 模型文件加载模型
func LoadXGBoostModel(path string) (*XGBoostModel, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读取XGBoost模型失败: %w", err)
	}
	var doc struct {
		Learner struct {
			Objective struct {
				Name string `json:"name"`
			} `json:"objective"`
			LearnerModelParam struct {
				NumClass  json.RawMessage `json:"num_class"`
				BaseScore json.RawMessage `json:"base_score"`
			} `json:"learner_model_param"`
			GradientBooster struct {
				Model struct {
					Trees []xgTree `json:"trees"`
				} `json:"model"`
			} `json:"gradient_booster"`
		} `json:"learner"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("解析XGBoost模型JSON失败: %w", err)
	}

	numClass := 1
	if len(doc.Learner.LearnerModelParam.NumClass) > 0 {
		if n, err := parseIntValue(doc.Learner.LearnerModelParam.NumClass); err == nil && n > 0 {
			numClass = n
		}
	}
	baseScore := 0.5
	if len(doc.Learner.LearnerModelParam.BaseScore) > 0 {
		if bs, err := parseFloatValue(doc.Learner.LearnerModelParam.BaseScore); err == nil {
			baseScore = bs
		}
	}

	trees := doc.Learner.GradientBooster.Model.Trees
	if len(trees) == 0 {
		return nil, fmt.Errorf("XGBoost模型不含任何树")
	}
	m := &XGBoostModel{
		trees:         trees,
		numClass:      numClass,
		treesPerClass: len(trees) / numClass,
		baseScore:     baseScore,
	}
	return m, nil
}

// parseIntValue 解析 JSON 值（可能是数字或带引号字符串）
func parseIntValue(raw json.RawMessage) (int, error) {
	s := strings.TrimSpace(string(raw))
	if len(s) > 0 && s[0] == '"' {
		if u, err := strconv.Unquote(s); err == nil {
			s = u
		}
	}
	return strconv.Atoi(s)
}

// parseFloatValue 解析 JSON 值（可能是数字或带引号字符串，如 "5E-1"）
func parseFloatValue(raw json.RawMessage) (float64, error) {
	s := strings.TrimSpace(string(raw))
	if len(s) > 0 && s[0] == '"' {
		if u, err := strconv.Unquote(s); err == nil {
			s = u
		}
	}
	return strconv.ParseFloat(s, 64)
}

// NumClass 返回类别数
func (m *XGBoostModel) NumClass() int { return m.numClass }

// PredictRawMargin 计算各类别原始 margin（未做 softmax）。
// feats 需为已缩放（StandardScaler）后的特征向量。
func (m *XGBoostModel) PredictRawMargin(feats []float64) []float64 {
	margins := make([]float64, m.numClass)
	for k := 0; k < m.numClass; k++ {
		// XGBoost（hist 方法 + float32）内部以 float32 累加叶值，Go 端须同样
		// 以 float32 累加才能与 Python 预测结果逐位一致。
		var s float32
		for t := 0; t < m.treesPerClass; t++ {
			// 树按类别交错排列：第 k 类第 t 棵树的下标为 t*numClass+k
			tree := &m.trees[t*m.numClass+k]
			node := 0
			for tree.LeftChildren[node] != -1 {
				f := tree.SplitIndices[node]
				v := math.NaN()
				if f >= 0 && f < len(feats) {
					// XGBoost 内部以 float32 存储特征与阈值并比较（float32(value) < float32(threshold)）。
					// 特征值必须先转 float32，阈值也必须转 float32 再比较；否则在
					// v≈threshold 边界（float64 略小、float32 相等）时方向会不一致。
					v = float64(float32(feats[f]))
				}
				if math.IsNaN(v) {
					// 缺失值：按 default_left 决定走向
					if tree.DefaultLeft[node] == 1 {
						node = tree.LeftChildren[node]
					} else {
						node = tree.RightChildren[node]
					}
				} else if float32(v) < float32(tree.SplitConditions[node]) {
					node = tree.LeftChildren[node]
				} else {
					node = tree.RightChildren[node]
				}
			}
			s += float32(tree.SplitConditions[node]) // 叶节点值（float32 累加）
		}
		margins[k] = float64(s)
	}
	for k := 0; k < m.numClass; k++ {
		margins[k] += m.baseScore
	}
	return margins
}

// PredictClass 预测类别索引（0..numClass-1）
func (m *XGBoostModel) PredictClass(feats []float64) int {
	margins := m.PredictRawMargin(feats)
	best, idx := margins[0], 0
	for k := 1; k < len(margins); k++ {
		if margins[k] > best {
			best, idx = margins[k], k
		}
	}
	return idx
}

// PredictProba softmax 概率（与其余 ML 模型接口一致）
func (m *XGBoostModel) PredictProba(feats []float64) []float64 {
	return softmax(m.PredictRawMargin(feats))
}

// StandardScaler 标准缩放器（与 sklearn StandardScaler 对齐）
type StandardScaler struct {
	Mean  []float64 `json:"mean_"`
	Scale []float64 `json:"scale_"`
}

// LoadStandardScalerJSON 从 JSON 文件加载 StandardScaler（mean_/scale_ 数组）
func LoadStandardScalerJSON(path string) (*StandardScaler, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读取StandardScaler失败: %w", err)
	}
	var sc StandardScaler
	if err := json.Unmarshal(raw, &sc); err != nil {
		return nil, fmt.Errorf("解析StandardScaler失败: %w", err)
	}
	if len(sc.Mean) == 0 || len(sc.Scale) == 0 || len(sc.Mean) != len(sc.Scale) {
		return nil, fmt.Errorf("StandardScaler mean_/scale_ 长度无效")
	}
	return &sc, nil
}

// Transform 标准化：x' = (x - mean) / scale
func (s *StandardScaler) Transform(feats []float64) []float64 {
	out := make([]float64, len(feats))
	for i := range feats {
		if i < len(s.Mean) && i < len(s.Scale) {
			out[i] = (feats[i] - s.Mean[i]) / s.Scale[i]
		} else {
			out[i] = feats[i]
		}
	}
	return out
}
