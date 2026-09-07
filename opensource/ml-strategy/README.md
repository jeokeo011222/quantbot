# A股机器学习策略包（Quant ML Strategy）

A股日线行情驱动的机器学习选股策略：**Python 训练 + 纯 Go 推理**，无 Python 运行时依赖即可线上预测。

- 特征：51 维技术指标（量价 / 均线 / RSI / MACD / KDJ / BOLL / 量能 / K线形态 / 极值）
- 模型：XGBoost / LightGBM / 随机森林 / 逻辑回归 / MLP，支持多模型集成（概率平均 / 投票）
- 标签：三分类（0 卖出 / 1 观望 / 2 买入），由未来 N 日收益与阈值生成
- 推理：纯 Go 解析 JSON 模型权重，StandardScaler 配套缩放，特征两端逐字段对齐

## 目录结构

```
ml-strategy/
├── python/
│   ├── train_from_duckdb.py   # 训练脚本：DuckDB 日线 → 5 模型权重
│   └── ml_service.py          # Flask 推理服务（训练/预测/回测/模型管理 API）
├── go/
│   ├── go.mod                 # 独立 Go module，零外部依赖
│   ├── models/                # 纯 Go 推理引擎（特征工程 + 各模型解析器）
│   └── example/               # 可运行示例：合成K线 → 特征 → 预测
├── docs/
│   ├── strategy_integration.md # 集成指南（特征→缩放→预测→下单）
│   └── tuning.md               # 调参指南（特征/标签/超参/集成/评估）
├── requirements.txt
└── LICENSE                    # Apache-2.0
```

## 快速开始

### 1. 安装依赖

```bash
pip install -r requirements.txt
```

### 2. 准备数据

在 DuckDB 中建表（库名任意），`stock.duckdb` 或 `--db` 指定路径：

```sql
CREATE TABLE ohlc (
    symbol VARCHAR, date DATE,
    open DOUBLE, high DOUBLE, low DOUBLE, close DOUBLE,
    volume BIGINT, amount DOUBLE
);
```

字段说明：`symbol` 带市场前缀（如 `sz000001` / `sh600000`），`amount` 为当日成交额（元）。

### 3. 训练

```bash
# 默认 5 模型
python python/train_from_duckdb.py --models xgboost,lgbm,rf,logistic,mlp

# 指定输出目录与标签参数
python python/train_from_duckdb.py --models lgbm,rf --output-dir models \
    --forward-days 5 --threshold 0.02 --top-n 500
```

输出（每个模型一套，Go 推理器可直接解析）：

```
{name}.json         模型权重
{name}_scaler.json  StandardScaler（mean_/scale_）
{name}_info.json    元数据（model_type / feature_columns / 训练指标）
```

### 4. 推理

```bash
cd go
go run ./example
```

或启用 Python HTTP 服务（`http://127.0.0.1:8766`，见脚本内 API 说明）：

```bash
python python/ml_service.py
```

## Go 推理引擎

`go/models` 为纯标准库实现（无第三方依赖），三分钟接入：

```go
feats := models.ComputeXGBFeatures(ohlc)                  // 51 维特征
m, _  := models.LoadMLModelByName(dir, "astock_lgbm_v1")  // 加载模型
sc, _ := models.LoadStandardScalerForModel(dir, name)     // 加载缩放器
cls, proba := m.PredictClass(sc.Transform(feats[len(feats)-1])),
              m.PredictProba(sc.Transform(feats[len(feats)-1]))
```

详见 [docs/strategy_integration.md](docs/strategy_integration.md)。

## 与 QuantBot 的关系

本包是 [QuantBot](https://github.com/jeokeo011222/quantbot) 桌面量化工作台「机器学习策略」的开源子集：

- 训练程序与 Go 推理引擎完整开源（Apache-2.0）
- QuantBot 内部在该包之上增加了回测引擎、因子复盘、六维判势仓位控制、风控与多 Agent 决策编排（保持闭源）
- 本包独立可用，不依赖 QuantBot 任何闭源代码

## 免责声明

本仓库仅供学习与研究，不构成任何投资建议。量化策略存在失效风险，使用前请充分验证。行情数据请使用合规渠道获取。
