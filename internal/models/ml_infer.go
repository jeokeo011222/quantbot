package models

// 统一机器学习模型推理接口（纯 Go，无 Python 依赖）。
// 各模型类型（XGBoost / LightGBM / 随机森林 / 逻辑回归 / MLP）均实现
// PredictClass / PredictProba / NumClass，供策略层以统一方式做多模型集成
// （投票 / 概率平均）。
//
// 模型文件约定（由 python/train_from_duckdb.py 统一导出）：
//   {name}.json        - 模型权重（各模型自定义 JSON，Go 可解析）
//   {name}_scaler.json - StandardScaler (mean_/scale_)
//   {name}_info.json   - 元数据，其中 model_type 字段决定采用哪个解析器
//
// 特征顺序必须与 features_go.go xgbFeatureCols（51 维）一致；
// 推理前必须用对应 _scaler.json 的 StandardScaler 做缩放（与训练一致）。

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// MLModel 统一机器学习模型接口
type MLModel interface {
	// PredictClass 预测类别索引（0=卖出 1=观望 2=买入）
	PredictClass(feats []float64) int
	// PredictProba 预测各类别概率（长度 = NumClass()，和为 1）
	PredictProba(feats []float64) []float64
	// NumClass 类别数
	NumClass() int
}

// modelInfo 模型元数据（{name}_info.json）
type modelInfo struct {
	ModelName     string   `json:"model_name"`
	ModelType     string   `json:"model_type"`
	FeatureColumns []string `json:"feature_columns"`
	LabelNames    []string `json:"label_names"`
}

// readModelInfo 读取模型元数据；兼容旧版 xgboost_astock_v1（无 model_type 字段，默认 xgboost）
func readModelInfo(infoPath string) (*modelInfo, error) {
	raw, err := os.ReadFile(infoPath)
	if err != nil {
		return nil, fmt.Errorf("读取模型信息失败: %w", err)
	}
	var info modelInfo
	if err := json.Unmarshal(raw, &info); err != nil {
		return nil, fmt.Errorf("解析模型信息失败: %w", err)
	}
	if info.ModelType == "" {
		info.ModelType = "xgboost"
	}
	if info.ModelName == "" {
		info.ModelName = filepath.Base(filepath.Dir(infoPath)) // 兜底
	}
	return &info, nil
}

// LoadMLModelByName 从模型目录加载指定名称的模型（读取 _info.json 判断模型类型后分发）。
func LoadMLModelByName(modelDir, name string) (MLModel, error) {
	if modelDir == "" || name == "" {
		return nil, fmt.Errorf("模型目录/名称不能为空")
	}
	info, err := readModelInfo(filepath.Join(modelDir, name+"_info.json"))
	if err != nil {
		return nil, err
	}
	modelPath := filepath.Join(modelDir, name+".json")
	switch info.ModelType {
	case "xgboost":
		return LoadXGBoostModel(modelPath)
	case "lgbm":
		return LoadLGBMModel(modelPath)
	case "rf":
		return LoadRFModel(modelPath)
	case "logistic", "mlp":
		return LoadLinearModel(modelPath)
	default:
		return nil, fmt.Errorf("不支持的模型类型: %s", info.ModelType)
	}
}

// LoadStandardScalerForModel 加载模型配套的 StandardScaler（{name}_scaler.json）
func LoadStandardScalerForModel(modelDir, name string) (*StandardScaler, error) {
	return LoadStandardScalerJSON(filepath.Join(modelDir, name+"_scaler.json"))
}
