# 机器学习策略调参指南

## 1. 特征体系（51 维）

训练与推理共用一套特征（`python/ml_service.py` 的 FeatureEngineer ↔ Go 端 `xgbFeatureCols`）：

| 分组 | 特征 | 说明 |
|---|---|---|
| 量价原始 | open/high/low/close/volume/amount | 6 维 |
| 收益 | return_1d/5d/10d/20d | 多周期动量 |
| 波动率 | volatility_5/10/20 | rolling std (ddof=1) |
| 均线 | ma_5/10/20/60 + ma_ratio_* + ma_alignment | 价格相对均线位置与均线多头排列 |
| 动量指标 | rsi_6/14 | 超买超卖 |
| 趋势指标 | macd_dif/dea/hist/cross | 金叉死叉 |
| 摆动指标 | kdj_k/d/j | 随机指标 |
| 布林带 | boll_mid/std/upper/lower/position | 带宽与位置 |
| 量能 | vol_ma_5/10/20, vol_ratio, vol_surge | 量比与放量 |
| K线形态 | body_ratio, upper/lower_shadow, is_yang | 实体/影线/阴阳 |
| 极值 | high_20d/low_20d/new_high/new_low | 20日新高新低 |

**要点**：
- pandas 语义对齐（rolling/ewm/pct_change 的窗口与 NaN 处理）是 Go 端与 Python 端一致性的关键，改动特征必须两端同步
- 所有 NaN 统一 fillna(0)

## 2. 标签映射与训练目标

三分类（0 卖出 / 1 观望 / 2 买入），标签由未来 N 日收益相对阈值生成：

- 未来 N 日收益 ≥ +T → 买入
- 未来 N 日收益 ≤ −T → 卖出
- 其余 → 观望

由 CLI 参数控制：`--forward-days`（预测未来天数，默认 5）、`--threshold`（涨跌幅标签阈值，默认 0.02）。调参时优先关注两者的匹配（短 N 高换手、长 N 信号滞后）。

## 3. 超参数调整

训练脚本内各模型的 `default_params` 硬编码在 `train_from_duckdb.py`（如随机森林 `n_estimators=150, max_depth=8, min_samples_leaf=20`、逻辑回归 `alpha=1e-4`、MLP `batch_size=512, learning_rate_init=1e-3`）。调整方式：

1. 编辑脚本中对应模型的 `default_params` 字典后重新训练
2. 训练脚本对树模型默认做交叉验证（`cv_model`），日志会输出验证集指标，用指标对比不同超参组合

**过拟合防范（最重要）**：
1. **时序切分**：按日期前 80% 训练 / 后 20% 验证，禁止 `train_test_split` 随机乱序切分（A股收益有强时序性，乱序切分会高估泛化）
2. 树模型先限制深度/叶数，观察验证集 f1 是否随训练集提升同步上升
3. 用未来 N 日收益打标签本身即引入重叠样本，需接受一定偏差或做样本去重

## 4. 多模型集成

默认 5 模型集成，两种方式（见 QuantBot `strategies` 表的 `ensemble_method`）：

| 方式 | 说明 | 适用 |
|---|---|---|
| `probability`（默认） | 各模型输出概率向量求平均，取 argmax | 概率校准较好时 |
| `voting` | 各模型投票，多数决定 | 模型差异大时更稳 |

集成能显著降低单模型的偶发噪声；若某模型验证集 f1 长期垫底，建议从集成中剔除。

## 5. 评估口径

训练脚本输出 accuracy / precision / recall / f1（按验证集）。实盘验证建议再看：

- **信号切换后的收益**（买入信号后 N 日收益 vs 全样本基准）
- **换手率**（信号频繁在 0↔2 抖动说明 N 太小或阈值太紧）
- **分年度收益**（识别风格依赖：单一年份好 ≠ 长期有效）

## 6. 与因子复盘的联动

在 QuantBot 中，ML 策略信号同样受盘前六维判势（position_rate 缩放）与风险控制约束——模型只管「方向」，仓位与风控交给判势层，避免模型在极端行情下满仓。
