# 机器学习策略的行情策略集成指南

本仓库提供训练与推理组件，将其接入你的行情/回测系统形成完整「机器学习策略」只需三步：

1. **特征**：`models.ComputeXGBFeatures(ohlc)` 从K线序列计算 51 维技术特征（与训练端逐字段对齐）
2. **缩放**：用训练产出的 `{name}_scaler.json`（StandardScaler）对最新一根K线的特征做标准化（必须与训练一致，否则预测错乱）
3. **预测**：`models.LoadMLModelByName` 加载模型，`PredictClass` 输出 0=卖出 / 1=观望 / 2=买入

## 集成骨架（Go）

```go
import (
    "github.com/jeokeo011222/quantbot/opensource/ml-strategy/go/models"
)

// 每根K线收盘后调用，生成下一个交易日的信号
func signalFromOHLC(ohlc models.XGBOHLCV, modelDir, modelName string) (int, []float64) {
    feats := models.ComputeXGBFeatures(ohlc)          // 全部样本的 51 维特征
    m, err := models.LoadMLModelByName(modelDir, modelName)
    if err != nil {
        return 1, nil // 模型不可用 → 观望
    }
    sc, err := models.LoadStandardScalerForModel(modelDir, modelName)
    if err != nil {
        return 1, nil
    }
    last := sc.Transform(feats[len(feats)-1])         // 仅最新一根K线
    return m.PredictClass(last), m.PredictProba(last) // 0卖出 1观望 2买入
}

// 多模型集成（概率平均）
func ensembleSignal(ohlc models.XGBOHLCV, modelDir string, names []string) int {
    proba := [3]float64{}
    ok := 0
    for _, name := range names {
        cls, p := signalFromOHLC(ohlc, modelDir, name)
        if cls < 0 || len(p) != 3 {
            continue
        }
        for c := 0; c < 3; c++ {
            proba[c] += p[c]
        }
        ok++
    }
    if ok == 0 {
        return 1
    }
    best := 0
    for c := 1; c < 3; c++ {
        if proba[c] > proba[best] {
            best = c
        }
    }
    return best
}
```

## 模型目录约定（与 QuantBot 一致）

按以下顺序查找（路径存在即用）：

```
exeDir/models → exeDir/data/models → 仓库根/models → cwd/models
```

## 信号到下单（参考）

- `0 卖出`：仅当持仓存在时触发清仓（策略信号优先于市场状态机）
- `1 观望`：维持现状，不交易
- `2 买入`：生成买入订单，仓位按系统 position_rate 缩放

## 注意事项

1. **特征顺序不可变**：Go 端 `xgbFeatureCols` 与 Python 端 `feature_columns` 必须一致（51 维），否则预测错乱
2. **scaler 必须配套**：每换一次模型权重，必须同步换对应的 `_scaler.json`（训练时已按该 scaler 标准化）
3. **标签映射**：训练脚本按未来 N 日收益打标签（0 卖出 / 1 观望 / 2 买入），推理标签含义必须与训练一致
4. **无 Python 依赖**：Go 推理器只读 JSON 权重文件，线上部署无需 Python 环境
