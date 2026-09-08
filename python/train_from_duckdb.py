"""
从 DuckDB 提取A股日线数据训练多种机器学习模型（XGBoost / LightGBM / 随机森林 / 逻辑回归 / MLP）

数据库路径: python/stock.duckdb（表 ohlc：symbol, date, open, high, low, close, volume, amount）

统一输出（供 Go 端纯 Go 推理，无需 Python）：
    {name}.json        - 模型权重（各模型自定义 JSON，Go 可解析）
    {name}_scaler.json - StandardScaler (mean_/scale_/n_features_in_/feature_names_in_)
    {name}_info.json   - 元数据（model_type / feature_columns / 训练指标 / 标签映射）

使用：
    python train_from_duckdb.py --models xgboost,lgbm,rf,logistic,mlp
    python train_from_duckdb.py --models lgbm,rf --output-dir ../build/bin/models
"""

import argparse
import json
import os
import sys
import time
import warnings
from typing import Dict, List, Optional

import numpy as np
import pandas as pd

warnings.filterwarnings('ignore')

# 尝试导入 duckdb
try:
    import duckdb
    DUCKDB_AVAILABLE = True
except ImportError:
    DUCKDB_AVAILABLE = False
    print("[WARN] duckdb not installed. Run: pip install duckdb")

# 尝试导入 ML 依赖
try:
    import xgboost as xgb
    import lightgbm as lgb
    from sklearn.ensemble import RandomForestClassifier
    from sklearn.linear_model import LogisticRegression
    from sklearn.neural_network import MLPClassifier
    from sklearn.model_selection import cross_val_score
    from sklearn.preprocessing import StandardScaler
    from sklearn.metrics import accuracy_score, precision_score, recall_score, f1_score
    import joblib
    ML_AVAILABLE = True
except ImportError as e:
    ML_AVAILABLE = False
    print(f"[WARN] ML dependencies missing: {e}")
    print("  Install: pip install xgboost lightgbm scikit-learn pandas numpy joblib duckdb")


# 数据库路径
DB_PATH = os.path.join(os.path.dirname(os.path.abspath(__file__)), "stock.duckdb")
DEFAULT_MODEL_DIR = os.path.normpath(os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "internal", "models"))

# ============================================================
# 特征列规范顺序（必须与 Go 端 internal/models/features_go.go 的
# xgbFeatureCols 完全一致，共 51 维，顺序即模型特征列顺序）
# ============================================================
FEATURE_COLS = [
    "open", "high", "low", "close", "volume", "amount",
    "return_1d", "return_5d", "return_10d", "return_20d",
    "volatility_5", "volatility_10", "volatility_20",
    "ma_5", "ma_ratio_5", "ma_10", "ma_ratio_10", "ma_20", "ma_ratio_20", "ma_60", "ma_ratio_60",
    "ma_alignment",
    "rsi_6", "rsi_14",
    "macd_dif", "macd_dea", "macd_hist", "macd_cross",
    "kdj_k", "kdj_d", "kdj_j",
    "boll_mid", "boll_std", "boll_upper", "boll_lower", "boll_position",
    "vol_ma_5", "vol_ma_10", "vol_ma_20", "vol_ratio", "vol_surge",
    "body_ratio", "upper_shadow", "lower_shadow", "is_yang",
    "momentum_10", "momentum_20",
    "high_20d", "low_20d", "new_high", "new_low",
]

# 标签映射：0=卖出(-1) 1=观望(0) 2=买入(+1)
LABEL_NAMES = ["sell", "hold", "buy"]


# ============================================================
# 特征工程（与 Go 端对齐；显式按 FEATURE_COLS 排序）
# ============================================================

class FeatureEngineer:
    """特征工程"""

    @staticmethod
    def calculate_features(df: pd.DataFrame, fill_na: bool = True) -> pd.DataFrame:
        """计算技术指标特征（输出列顺序固定为 FEATURE_COLS）
        fill_na=True 时填充 0（与 Go 端推理一致）；False 时保留 NaN，用于在训练时识别并剔除 warm-up 行。
        """
        data = df.copy()
        cols_to_lower = {c: c.lower() for c in data.columns}
        data = data.rename(columns=cols_to_lower)

        # 0. 基础价格/成交列（保证顺序，独立于表结构）
        base = pd.DataFrame(index=data.index)
        for c in ["open", "high", "low", "close", "volume", "amount"]:
            base[c] = data[c] if c in data.columns else 0.0

        # 1. 基础价格特征
        base['return_1d'] = base['close'].pct_change(1)
        base['return_5d'] = base['close'].pct_change(5)
        base['return_10d'] = base['close'].pct_change(10)
        base['return_20d'] = base['close'].pct_change(20)

        # 2. 波动率特征
        base['volatility_5'] = base['return_1d'].rolling(5).std()
        base['volatility_10'] = base['return_1d'].rolling(10).std()
        base['volatility_20'] = base['return_1d'].rolling(20).std()

        # 3. 均线特征
        for period in [5, 10, 20, 60]:
            base[f'ma_{period}'] = base['close'].rolling(period).mean()
            base[f'ma_ratio_{period}'] = base['close'] / base[f'ma_{period}'] - 1

        # 4. 均线多头/空头排列
        base['ma_alignment'] = (
            (base['ma_5'] > base['ma_10']).astype(int) +
            (base['ma_10'] > base['ma_20']).astype(int) +
            (base['ma_20'] > base['ma_60']).astype(int)
        )

        # 5. RSI 特征
        base['rsi_6'] = FeatureEngineer._calculate_rsi(base['close'], 6)
        base['rsi_14'] = FeatureEngineer._calculate_rsi(base['close'], 14)

        # 6. MACD 特征
        ema12 = base['close'].ewm(span=12, adjust=False).mean()
        ema26 = base['close'].ewm(span=26, adjust=False).mean()
        base['macd_dif'] = ema12 - ema26
        base['macd_dea'] = base['macd_dif'].ewm(span=9, adjust=False).mean()
        base['macd_hist'] = 2 * (base['macd_dif'] - base['macd_dea'])
        base['macd_cross'] = np.where(
            (base['macd_dif'] > base['macd_dea']) & (base['macd_dif'].shift(1) <= base['macd_dea'].shift(1)),
            1,
            np.where(
                (base['macd_dif'] < base['macd_dea']) & (base['macd_dif'].shift(1) >= base['macd_dea'].shift(1)),
                -1, 0
            )
        )

        # 7. KDJ 特征
        low_min = base['low'].rolling(9).min()
        high_max = base['high'].rolling(9).max()
        rsv = (base['close'] - low_min) / (high_max - low_min + 1e-10) * 100
        base['kdj_k'] = rsv.ewm(com=2, adjust=False).mean()
        base['kdj_d'] = base['kdj_k'].ewm(com=2, adjust=False).mean()
        base['kdj_j'] = 3 * base['kdj_k'] - 2 * base['kdj_d']

        # 8. 布林带特征
        base['boll_mid'] = base['close'].rolling(20).mean()
        base['boll_std'] = base['close'].rolling(20).std()
        base['boll_upper'] = base['boll_mid'] + 2 * base['boll_std']
        base['boll_lower'] = base['boll_mid'] - 2 * base['boll_std']
        base['boll_position'] = (base['close'] - base['boll_lower']) / (base['boll_upper'] - base['boll_lower'] + 1e-10)

        # 9. 成交量特征
        base['vol_ma_5'] = base['volume'].rolling(5).mean()
        base['vol_ma_10'] = base['volume'].rolling(10).mean()
        base['vol_ma_20'] = base['volume'].rolling(20).mean()
        base['vol_ratio'] = base['volume'] / base['vol_ma_20']
        base['vol_surge'] = np.where(base['volume'] > 2 * base['vol_ma_20'], 1, 0)

        # 10. 价格形态特征
        base['body_ratio'] = (base['close'] - base['open']) / (base['high'] - base['low'] + 1e-10)
        base['upper_shadow'] = (base['high'] - np.maximum(base['open'], base['close'])) / (base['high'] - base['low'] + 1e-10)
        base['lower_shadow'] = (np.minimum(base['open'], base['close']) - base['low']) / (base['high'] - base['low'] + 1e-10)
        base['is_yang'] = (base['close'] > base['open']).astype(int)

        # 11. 动量特征
        base['momentum_10'] = base['close'] / base['close'].shift(10) - 1
        base['momentum_20'] = base['close'] / base['close'].shift(20) - 1

        # 12. 新高/新低特征
        base['high_20d'] = base['high'].rolling(20).max()
        base['low_20d'] = base['low'].rolling(20).min()
        base['new_high'] = (base['close'] >= base['high_20d']).astype(int)
        base['new_low'] = (base['close'] <= base['low_20d']).astype(int)

        # 13. 按规范顺序输出（缺失列补 0），fill_na=True 时 fillna(0) 与 Go 端一致；
        #      fill_na=False 时保留 NaN 以便训练阶段剔除 warm-up（滚动窗口不足导致的 NaN）行。
        out = pd.DataFrame(index=data.index)
        for c in FEATURE_COLS:
            if c in base.columns:
                out[c] = base[c]
            else:
                out[c] = 0.0
        if fill_na:
            out = out.fillna(0.0)
        return out

    @staticmethod
    def _calculate_rsi(prices: pd.Series, period: int) -> pd.Series:
        """计算RSI"""
        delta = prices.diff()
        gain = (delta.where(delta > 0, 0)).rolling(window=period).mean()
        loss = (-delta.where(delta < 0, 0)).rolling(window=period).mean()
        rs = gain / (loss + 1e-10)
        return 100 - (100 / (1 + rs))

    @staticmethod
    def create_labels(df: pd.DataFrame, forward_days: int = 5, threshold: float = 0.02) -> pd.Series:
        """创建训练标签：未来 forward_days 涨幅>threshold 为买入(1)，<-threshold 为卖出(-1)，否则观望(0)。

        尾部 forward_days 行无未来收益（future_return 为 NaN），标签保持 NaN，
        由调用方剔除，绝不把它们误标为 hold(0) 混入训练。
        """
        future_return = df['close'].shift(-forward_days) / df['close'] - 1
        labels = pd.Series(np.nan, index=df.index)
        labels[(future_return > threshold)] = 1
        labels[(future_return < -threshold)] = -1
        labels[(future_return >= -threshold) & (future_return <= threshold)] = 0
        return labels

    @staticmethod
    def prepare_panel(df: pd.DataFrame, forward_days: int = 5, threshold: float = 0.02):
        """按 symbol 分组计算特征与标签，然后拼接成面板数据。

        解决原实现把多只股票拼接后直接算滚动特征/标签导致的跨股票污染：
          - 特征在每只股票内部独立计算（warm-up 行保留 NaN 后剔除）；
          - 标签在每只股票内部独立 shift(-forward_days)，尾部无未来收益的行剔除；
          - 返回 (X, y, dates)：y 已映射到 0/1/2，X 仅含有效样本。
        """
        df = df.sort_values(['symbol', 'date']).reset_index(drop=True)
        X_parts, y_parts, date_parts = [], [], []
        for _, g in df.groupby('symbol', sort=False):
            feat = FeatureEngineer.calculate_features(g, fill_na=False)
            valid_feat = feat.dropna().index  # 剔除 warm-up（滚动窗口不足的 NaN 特征）行
            lab = FeatureEngineer.create_labels(g.loc[valid_feat], forward_days, threshold)
            lab = lab.dropna()  # 剔除尾部无未来收益的行
            keep = lab.index
            if len(keep) == 0:
                continue
            X_parts.append(feat.loc[keep])
            y_parts.append(lab.loc[keep])
            date_parts.append(g['date'].loc[keep])
        if not X_parts:
            return (pd.DataFrame(columns=FEATURE_COLS), pd.Series(dtype=float), pd.Series(dtype='datetime64[ns]'))
        X = pd.concat(X_parts).reindex(columns=FEATURE_COLS).fillna(0.0).astype(np.float64)
        y = (pd.concat(y_parts).astype(int) + 1).reset_index(drop=True)  # -1→0, 0→1, 1→2
        dates = pd.to_datetime(pd.concat(date_parts)).reset_index(drop=True)
        return X, y, dates


# ============================================================
# 模型导出（统一 JSON 格式，Go 端可解析）
# ============================================================

def export_xgboost(model, path: str):
    """XGBoost save_model JSON（Go 端 xgboost_go.go 原生解析）"""
    model.save_model(path)


def export_lgbm(model, path: str):
    """LightGBM 导出 JSON（Go 端 lgbm_go.go 解析）
    注意：Booster.save_model 即使后缀 .json 也输出文本格式，必须用 dump_model() 取 dict 再序列化"""
    booster = model.booster_ if hasattr(model, 'booster_') else model
    payload = booster.dump_model()
    with open(path, 'w', encoding='utf-8') as f:
        json.dump(payload, f)


def export_sklearn_rf(model, path: str):
    """随机森林导出为自定义 JSON（树数组，Go 端 rf_go.go 解析）"""
    trees = []
    for est in model.estimators_:
        t = est.tree_
        trees.append({
            "children_left": [int(v) for v in t.children_left],
            "children_right": [int(v) for v in t.children_right],
            "feature": [int(v) for v in t.feature],
            "threshold": [float(v) for v in t.threshold],
            "values": [[float(v) for v in row] for row in t.value[:, 0, :]],
        })
    payload = {
        "n_classes": int(model.n_classes_),
        "n_estimators": len(trees),
        "trees": trees,
    }
    with open(path, 'w', encoding='utf-8') as f:
        json.dump(payload, f)


def export_logistic(model, path: str):
    """逻辑回归导出 coef_/intercept_（Go 端 linear_go.go 解析，softmax）"""
    payload = {
        "n_classes": int(model.coef_.shape[0]),
        "n_features": int(model.coef_.shape[1]),
        "coef": [[float(v) for v in row] for row in model.coef_],
        "intercept": [float(v) for v in model.intercept_],
    }
    with open(path, 'w', encoding='utf-8') as f:
        json.dump(payload, f)


def export_mlp(model, path: str):
    """MLP 导出 coefs_/intercepts_（Go 端 linear_go.go 解析，relu+softmax）"""
    payload = {
        "n_layers": int(model.n_layers_),
        "activation": str(model.activation),
        "coefs": [[[float(v) for v in row] for row in w] for w in model.coefs_],
        "intercepts": [[float(v) for v in b] for b in model.intercepts_],
    }
    with open(path, 'w', encoding='utf-8') as f:
        json.dump(payload, f)


def temporal_split(X, y, dates, test_size: float = 0.2, purge_days: int = 5):
    """按时间顺序划分训练/测试集（不再用随机 train_test_split），并做 purge。

    时间序列中随机切分会把未来的样本泄露进训练集。这里以日期切：测试集取时间上最晚的
    test_size 比例；训练集剔除切分点前 purge_days 天内的样本（其标签用到未来 forward_days，
    会窥探测试期信息）。返回 numpy 数组。
    """
    dates = np.array(pd.to_datetime(dates))
    uniq = np.unique(dates)
    cut_pos = int(len(uniq) * (1 - test_size))
    test_cut = uniq[cut_pos]
    purge = pd.Timedelta(days=purge_days)
    train_mask = dates < (test_cut - purge)
    test_mask = dates >= test_cut
    return (
        X[train_mask].values, y[train_mask].values,
        X[test_mask].values, y[test_mask].values,
        test_cut,
    )


# ============================================================
# 模型训练统一入口
# ============================================================

def train_model(
    df: pd.DataFrame,
    model_type: str = "xgboost",
    model_name: str = "astock_v1",
    forward_days: int = 5,
    threshold: float = 0.02,
    test_size: float = 0.2,
    params: Dict = None,
    output_dir: str = None,
) -> Dict:
    """
    训练指定类型模型并统一导出。

    Args:
        df: K线DataFrame（需包含 open/high/low/close/volume/amount）
        model_type: xgboost / lgbm / rf / logistic / mlp
        model_name: 输出模型名（生成 {model_name}.json / _scaler.json / _info.json）
    """
    if not ML_AVAILABLE:
        return {"error": "ML dependencies not installed"}
    if output_dir is None:
        output_dir = DEFAULT_MODEL_DIR

    # 按 symbol 分组计算特征与标签（避免跨股票污染），剔除 warm-up 与无未来标签的行
    print(f"\n  [{model_type}] Calculating per-symbol features & labels (forward={forward_days}d, threshold={threshold})...")
    engine = FeatureEngineer()
    X, y, dates = engine.prepare_panel(df, forward_days, threshold)
    print(f"  [{model_type}] Valid samples: {len(X)}, features: {X.shape[1]}")

    if len(X) < 1000:
        return {"error": f"样本不足: {len(X)} < 1000"}

    # 按时间先后划分训练/测试集（含 purge），避免随机切分把未来信息泄露进训练
    X_train, y_train, X_test, y_test, test_cut = temporal_split(
        X, y, dates, test_size=test_size, purge_days=forward_days
    )
    if len(X_train) < 100 or len(X_test) < 100:
        return {"error": f"时间切分后样本不足: train={len(X_train)}, test={len(X_test)}"}

    # 类别均衡样本权重：按类别逆频率加权，让模型不再把概率压向多数类（hold），
    # 从而在买/卖上有真实区分度，避免 argmax 恒落 hold 导致策略 0 成交、指标全 0。
    # 仅作用于 XGBoost / LightGBM（sklearn 模型直接用 class_weight）。
    def _balance_weights(yv):
        classes, counts = np.unique(yv, return_counts=True)
        w = dict(zip(classes, len(yv) / (len(classes) * counts)))
        return np.array([w[v] for v in yv], dtype=np.float64)
    train_sw = _balance_weights(y_train)

    print(f"  [{model_type}] 时间切分: train={len(X_train)}, test={len(X_test)}, test_cut={pd.Timestamp(test_cut).date()}")
    print(f"  [{model_type}] Label dist: sell={int((y == 0).sum())}, hold={int((y == 1).sum())}, buy={int((y == 2).sum())}")

    # 标准化（仅用训练集 fit，测试集 transform，避免测试信息泄漏）
    scaler = StandardScaler()
    X_train_s = scaler.fit_transform(X_train)
    X_test_s = scaler.transform(X_test)

    # ---- 构建模型 ----
    print(f"  [{model_type}] Training...")
    t0 = time.time()
    if model_type == "xgboost":
        default_params = {
            'max_depth': 6, 'learning_rate': 0.1, 'n_estimators': 200,
            'min_child_weight': 3, 'subsample': 0.8, 'colsample_bytree': 0.8,
            'reg_alpha': 0.1, 'reg_lambda': 1.0, 'objective': 'multi:softprob',
            'num_class': 3, 'eval_metric': 'mlogloss', 'random_state': 42,
            'tree_method': 'hist', 'use_label_encoder': False, 'verbosity': 0,
        }
        if params:
            default_params.update(params)
        model = xgb.XGBClassifier(**default_params)
        model.fit(X_train_s, y_train, sample_weight=train_sw, eval_set=[(X_test_s, y_test)], verbose=False)
        export_xgboost(model, os.path.join(output_dir, f"{model_name}.json"))
    elif model_type == "lgbm":
        default_params = {
            'objective': 'multiclass', 'num_class': 3, 'metric': 'multi_logloss',
            'num_leaves': 31, 'learning_rate': 0.1, 'n_estimators': 200,
            'min_child_samples': 20, 'subsample': 0.8, 'subsample_freq': 1,
            'colsample_bytree': 0.8, 'reg_alpha': 0.1, 'reg_lambda': 1.0,
            'random_state': 42, 'verbosity': -1, 'n_jobs': -1,
        }
        if params:
            default_params.update(params)
        model = lgb.LGBMClassifier(**default_params)
        model.fit(X_train_s, y_train, sample_weight=train_sw, eval_set=[(X_test_s, y_test)], callbacks=[lgb.early_stopping(20, verbose=False)])
        export_lgbm(model, os.path.join(output_dir, f"{model_name}.json"))
    elif model_type == "rf":
        default_params = {
            'n_estimators': 150, 'max_depth': 8, 'min_samples_leaf': 20,
            'max_features': 'sqrt', 'class_weight': 'balanced', 'random_state': 42, 'n_jobs': -1,
        }
        if params:
            default_params.update(params)
        model = RandomForestClassifier(**default_params)
        model.fit(X_train_s, y_train)
        export_sklearn_rf(model, os.path.join(output_dir, f"{model_name}.json"))
    elif model_type == "logistic":
        default_params = {'C': 1.0, 'solver': 'lbfgs', 'max_iter': 500, 'class_weight': 'balanced', 'random_state': 42}
        if params:
            default_params.update(params)
        model = LogisticRegression(**default_params)
        model.fit(X_train_s, y_train)
        export_logistic(model, os.path.join(output_dir, f"{model_name}.json"))
    elif model_type == "mlp":
        # 注意：sklearn 1.7.x 的 MLPClassifier 不支持 class_weight，
        # 均衡权重通过 fit 的 sample_weight 传入。
        default_params = {
            'hidden_layer_sizes': (64,), 'activation': 'relu', 'solver': 'adam',
            'alpha': 1e-4, 'batch_size': 512, 'learning_rate_init': 1e-3,
            'max_iter': 40, 'early_stopping': True, 'n_iter_no_change': 5,
            'validation_fraction': 0.1, 'random_state': 42,
        }
        if params:
            default_params.update(params)
        model = MLPClassifier(**default_params)
        model.fit(X_train_s, y_train, sample_weight=train_sw)
        export_mlp(model, os.path.join(output_dir, f"{model_name}.json"))
    else:
        return {"error": f"Unsupported model_type: {model_type}"}
    elapsed = time.time() - t0

    # 保存 scaler（JSON 供 Go 用，pkl 供 Python 复用）
    scaler_json = {
        "type": "StandardScaler",
        "mean_": [float(v) for v in scaler.mean_],
        "scale_": [float(v) for v in scaler.scale_],
        "n_features_in_": int(scaler.n_features_in_),
        "feature_names_in_": FEATURE_COLS,
    }
    with open(os.path.join(output_dir, f"{model_name}_scaler.json"), 'w', encoding='utf-8') as f:
        json.dump(scaler_json, f)
    joblib.dump(scaler, os.path.join(output_dir, f"{model_name}_scaler.pkl"))

    # ---- 评估 ----
    y_pred_test = model.predict(X_test_s)
    results = {
        'model_name': model_name,
        'model_type': model_type,
        'created_at': time.strftime('%Y-%m-%d %H:%M:%S'),
        'params': dict(default_params),
        'feature_columns': FEATURE_COLS,
        'label_names': LABEL_NAMES,  # 索引 0/1/2 -> sell/hold/buy
        'forward_days': forward_days,
        'threshold': threshold,
        'train_accuracy': float(round(accuracy_score(y_train, model.predict(X_train_s)), 4)),
        'test_accuracy': float(round(accuracy_score(y_test, y_pred_test), 4)),
        'test_precision_macro': float(round(precision_score(y_test, y_pred_test, average='macro', zero_division=0), 4)),
        'test_recall_macro': float(round(recall_score(y_test, y_pred_test, average='macro', zero_division=0), 4)),
        'test_f1_macro': float(round(f1_score(y_test, y_pred_test, average='macro', zero_division=0), 4)),
        'train_samples': int(len(y_train)),
        'test_samples': int(len(y_test)),
        'total_samples': int(len(X)),
        'train_seconds': round(elapsed, 1),
        'label_distribution': {
            'sell': int((y == 0).sum()),
            'hold': int((y == 1).sum()),
            'buy': int((y == 2).sum()),
        },
    }

    # 5折交叉验证（采样最多 8 万条控制耗时）
    if len(X) >= 500:
        print(f"  [{model_type}] Running 5-fold CV...")
        try:
            from sklearn.model_selection import cross_val_score
            if len(X) > 80000:
                idx = np.random.RandomState(42).choice(len(X), 80000, replace=False)
                X_cv, y_cv = X.iloc[idx], y.iloc[idx]
            else:
                X_cv, y_cv = X, y
            X_cv_s = scaler.transform(X_cv)
            if model_type == "lgbm":
                cv_model = lgb.LGBMClassifier(**{k: v for k, v in default_params.items() if k not in ('n_jobs',)})
            elif model_type == "rf":
                cv_model = RandomForestClassifier(**default_params)
            elif model_type == "logistic":
                cv_model = LogisticRegression(**default_params)
            elif model_type == "mlp":
                cv_model = MLPClassifier(**default_params)
            else:
                cv_model = xgb.XGBClassifier(**{k: v for k, v in default_params.items() if k != 'verbosity'})
            cv_scores = cross_val_score(cv_model, X_cv_s, y_cv, cv=5, scoring='accuracy', verbose=0, n_jobs=-1)
            results['cv_accuracy_mean'] = float(round(cv_scores.mean(), 4))
            results['cv_accuracy_std'] = float(round(cv_scores.std(), 4))
        except Exception as e:
            print(f"    CV failed: {e}")

    # 特征重要性（树模型）
    if model_type in ("xgboost", "lgbm", "rf"):
        try:
            importance = model.feature_importances_
            top_idx = np.argsort(importance)[::-1][:20]
            results['top_features'] = [
                {'feature': FEATURE_COLS[i], 'importance': float(round(importance[i], 4))}
                for i in top_idx
            ]
        except Exception:
            pass

    # ---- 统一导出信息文件 ----
    info_path = os.path.join(output_dir, f"{model_name}_info.json")
    with open(info_path, 'w', encoding='utf-8') as f:
        json.dump(results, f, ensure_ascii=False, indent=2)

    print(f"  [{model_type}] Saved: {model_name}.json / _scaler.json / _info.json  ({elapsed:.0f}s)")
    return results


# ============================================================
# 数据库操作
# ============================================================

def explore_database(db_path: str) -> Dict:
    """探索数据库结构"""
    if not DUCKDB_AVAILABLE:
        return {"error": "duckdb not installed"}
    conn = duckdb.connect(db_path, read_only=True)
    tables = conn.execute("""
        SELECT table_name FROM information_schema.tables WHERE table_schema = 'main' ORDER BY table_name
    """).fetchall()
    table_info = {}
    for table in tables:
        table_name = table[0]
        try:
            row_count = conn.execute(f"SELECT COUNT(*) FROM {table_name}").fetchone()[0]
            columns = conn.execute(f"""
                SELECT column_name, data_type FROM information_schema.columns
                WHERE table_name = '{table_name}' ORDER BY ordinal_position
            """).fetchall()
            table_info[table_name] = {
                "row_count": row_count,
                "columns": [{"name": c[0], "type": c[1]} for c in columns],
            }
            print(f"  Table: {table_name}  Rows: {row_count:,}  Cols: {len(columns)}")
        except Exception as e:
            print(f"  Table: {table_name} - Error: {e}")
    conn.close()
    return {"tables": table_info}


def get_all_stock_codes(db_path: str) -> List[str]:
    """获取数据库中所有股票代码"""
    if not DUCKDB_AVAILABLE:
        return []
    conn = duckdb.connect(db_path, read_only=True)
    codes = conn.execute("SELECT DISTINCT symbol FROM ohlc").fetchall()
    conn.close()
    return [c[0] for c in codes]


def main():
    parser = argparse.ArgumentParser(description="从 DuckDB 训练多种机器学习模型")
    parser.add_argument("--models", default="xgboost,lgbm,rf,logistic,mlp",
                        help="逗号分隔的模型列表: xgboost,lgbm,rf,logistic,mlp")
    parser.add_argument("--output-dir", default=None, help="模型输出目录（默认 internal/models）")
    parser.add_argument("--forward-days", type=int, default=5, help="预测未来天数")
    parser.add_argument("--threshold", type=float, default=0.02, help="涨跌幅标签阈值")
    parser.add_argument("--top-n", type=int, default=500, help="采样股票数（按历史数据量）")
    parser.add_argument("--min-bars", type=int, default=120, help="最少K线数")
    parser.add_argument("--db", default=None, help="DuckDB 路径（默认 python/stock.duckdb）")
    args = parser.parse_args()

    print("=" * 60)
    print("DuckDB → 多模型 ML 训练脚本")
    print("=" * 60)

    if not DUCKDB_AVAILABLE or not ML_AVAILABLE:
        print("[ERROR] 依赖不完整（duckdb / xgboost / lightgbm / scikit-learn）")
        return

    db_path = args.db or DB_PATH
    if not os.path.exists(db_path):
        print(f"[ERROR] Database not found: {db_path}")
        return

    output_dir = args.output_dir or DEFAULT_MODEL_DIR
    os.makedirs(output_dir, exist_ok=True)
    print(f"\nDatabase: {db_path}")
    print(f"Output dir: {output_dir}")
    print(f"Models: {args.models}")

    model_types = [m.strip() for m in args.models.split(",") if m.strip()]
    supported = {"xgboost", "lgbm", "rf", "logistic", "mlp"}
    model_types = [m for m in model_types if m in supported]
    if not model_types:
        print("[ERROR] 未指定有效模型")
        return

    # 1. 探索数据库
    print("\n" + "-" * 40)
    print("Step 1: 探索数据库...")
    explore_database(db_path)

    # 2. 提取数据（采样有足够历史数据的股票）
    print("\n" + "-" * 40)
    print("Step 2: 提取K线数据...")
    conn = duckdb.connect(db_path, read_only=True)
    stock_counts = conn.execute(f"""
        SELECT symbol, COUNT(*) as cnt FROM ohlc
        GROUP BY symbol HAVING COUNT(*) >= {args.min_bars}
        ORDER BY cnt DESC LIMIT {args.top_n}
    """).fetchall()
    sample_stocks = [row[0] for row in stock_counts]
    conn.close()
    print(f"  选择 {len(sample_stocks)} 只股票（>= {args.min_bars} 根K线）")

    if not sample_stocks:
        print("[ERROR] 无符合条件的股票")
        return

    codes_str = "', '".join(sample_stocks)
    conn = duckdb.connect(db_path, read_only=True)
    query = f"SELECT * FROM ohlc WHERE symbol IN ('{codes_str}') ORDER BY symbol, date"
    full_df = conn.execute(query).fetchdf()
    conn.close()
    full_df.columns = [c.lower() for c in full_df.columns]
    print(f"  总记录数: {len(full_df):,}  日期范围: {full_df['date'].min()} ~ {full_df['date'].max()}")

    # 3. 训练各模型
    print("\n" + "-" * 40)
    print("Step 3: 训练模型...")
    all_results = []
    for mt in model_types:
        # 模型文件名必须与 Go 端 internal/models 加载约定一致：
        #   xgboost_astock_v1 / astock_lgbm_v1 / astock_rf_v1 / astock_logistic_v1 / astock_mlp_v1
        # （Go 端 LoadMLModelByName 按这些 name 读 {name}.json / _scaler.json / _info.json）
        model_name = "xgboost_astock_v1" if mt == "xgboost" else f"astock_{mt}_v1"
        print(f"\n{'#'*60}\n# 训练 {mt}  ->  {model_name}\n{'#'*60}")
        res = train_model(
            full_df,
            model_type=mt,
            model_name=model_name,
            forward_days=args.forward_days,
            threshold=args.threshold,
            test_size=0.2,
            output_dir=output_dir,
        )
        if isinstance(res, dict) and 'error' not in res:
            all_results.append(res)

    # 4. 对比报表
    if all_results:
        print("\n" + "=" * 60)
        print("模型对比报表")
        print("=" * 60)
        header = f"{'模型':<28}{'测试ACC':>10}{'CV ACC':>12}{'F1':>8}{'样本':>10}{'耗时(s)':>9}"
        print(header)
        print("-" * len(header))
        for r in all_results:
            cv = f"{r.get('cv_accuracy_mean', 0):.4f}±{r.get('cv_accuracy_std', 0):.4f}"
            print(f"{r['model_name']:<28}{r['test_accuracy']:>10.4f}{cv:>14}{r['test_f1_macro']:>8.4f}"
                  f"{r['total_samples']:>10,}{r.get('train_seconds', 0):>9.0f}")
        comp_path = os.path.join(output_dir, "models_comparison.json")
        with open(comp_path, 'w', encoding='utf-8') as f:
            json.dump(all_results, f, ensure_ascii=False, indent=2)
        print(f"\n对比结果已保存: {comp_path}")

    print("\n" + "=" * 60)
    print("Done!")
    print("=" * 60)


if __name__ == '__main__':
    main()
