package models

// 纯 Go 实现的线性/浅层网络模型推理（sklearn 逻辑回归 & MLP）。
// 解析 python/train_from_duckdb.py 导出的自定义 JSON：
//
// 逻辑回归（多分类 = multinomial softmax）：
//   {"n_classes":3,"n_features":51,"coef":[[...]],"intercept":[...]}
//   proba_k = softmax(coef_k·x + intercept_k)
//
// MLP（默认 relu 隐藏层 + softmax 输出）：
//   {"n_layers":3,"activation":"relu","coefs":[[W1],[W2]],"intercepts":[[b1],[b2]]}
//   hidden = relu(x·W1 + b1)；out = hidden·W2 + b2；proba = softmax(out)

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
)

// linearDoc 逻辑回归 / MLP 共用 JSON 结构
type linearDoc struct {
	NClasses   int         `json:"n_classes"`
	NFeatures  int         `json:"n_features"`
	Coef       [][]float64 `json:"coef"`      // 逻辑回归 (n_classes, n_features)
	Intercept  []float64   `json:"intercept"` // 逻辑回归 (n_classes)
	NLayers    int         `json:"n_layers"`  // MLP 层数
	Activation string      `json:"activation"`
	Coefs      [][][]float64 `json:"coefs"`    // MLP 权重矩阵列表
	Intercepts [][]float64   `json:"intercepts"` // MLP 偏置向量列表
}

// LinearModel 逻辑回归或 MLP 模型（isMLP 区分）
type LinearModel struct {
	isMLP   bool
	nClass  int
	nFeat   int
	coef    [][]float64 // logistic
	interc  []float64   // logistic
	coefs   [][][]float64 // mlp
	bias    [][]float64 // mlp
}

// LoadLinearModel 从 JSON 加载逻辑回归或 MLP 模型
func LoadLinearModel(path string) (*LinearModel, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读取线性模型失败: %w", err)
	}
	var doc linearDoc
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("解析线性模型JSON失败: %w", err)
	}
	m := &LinearModel{}
	if len(doc.Coefs) > 0 {
		// MLP
		m.isMLP = true
		m.coefs = doc.Coefs
		m.bias = doc.Intercepts
		if len(doc.Coefs) == 0 {
			return nil, fmt.Errorf("MLP模型 coefs 为空")
		}
		if len(doc.Coefs[len(doc.Coefs)-1]) == 0 {
			return nil, fmt.Errorf("MLP模型输出权重为空")
		}
		m.nFeat = len(doc.Coefs[0])
		m.nClass = len(doc.Coefs[len(doc.Coefs)-1][0])
		if m.nClass <= 0 {
			return nil, fmt.Errorf("MLP模型类别数无效")
		}
	} else {
		// 逻辑回归
		m.nClass = doc.NClasses
		m.nFeat = doc.NFeatures
		m.coef = doc.Coef
		m.interc = doc.Intercept
		if m.nClass <= 0 || len(m.coef) == 0 {
			return nil, fmt.Errorf("逻辑回归模型 coef/n_classes 无效")
		}
		if len(m.coef[0]) < m.nFeat {
			m.nFeat = len(m.coef[0])
		}
	}
	return m, nil
}

// NumClass 返回类别数
func (m *LinearModel) NumClass() int { return m.nClass }

// rawScores 计算各类别原始分
func (m *LinearModel) rawScores(feats []float64) []float64 {
	if m.isMLP {
		return m.mlpForward(feats)
	}
	scores := make([]float64, m.nClass)
	for k := 0; k < m.nClass && k < len(m.coef); k++ {
		s := m.interc[k]
		row := m.coef[k]
		for i := 0; i < m.nFeat && i < len(feats) && i < len(row); i++ {
			s += row[i] * feats[i]
		}
		scores[k] = s
	}
	return scores
}

// mlpForward 浅层 MLP 前向传播（relu 隐藏层 + 线性输出）
func (m *LinearModel) mlpForward(feats []float64) []float64 {
	cur := feats
	for layer := 0; layer < len(m.coefs); layer++ {
		W := m.coefs[layer]
		b := m.bias[layer]
		next := make([]float64, len(W[0]))
		for j := range next {
			s := b[j]
			for i := 0; i < len(W) && i < len(cur); i++ {
				s += W[i][j] * cur[i]
			}
			if layer < len(m.coefs)-1 {
				// 隐藏层 relu
				if s < 0 {
					s = 0
				}
			}
			next[j] = s
		}
		cur = next
	}
	return cur // 输出层原始分（softmax 前）
}

// PredictProba softmax 概率
func (m *LinearModel) PredictProba(feats []float64) []float64 {
	return softmax(m.rawScores(feats))
}

// PredictClass argmax 类别索引
func (m *LinearModel) PredictClass(feats []float64) int {
	return argmaxIndex(m.rawScores(feats))
}

// 保证 math 被引用（mlpForward 使用 relu 阈值比较，无 math 调用时避免编译告警）
var _ = math.Max
