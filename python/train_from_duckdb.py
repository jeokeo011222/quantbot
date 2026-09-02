"""
从 DuckDB 数据库提取A股日线数据并训练XGBoost模型

数据库路径: python/stock.duckdb

使用：
    python train_from_duckdb.py
"""

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

# 尝试导入 xgboost 相关
try:
    import xgboost as xgb
    from sklearn.model_selection import train_test_split, cross_val_score
    from sklearn.preprocessing import StandardScaler
    from sklearn.metrics import (
        accuracy_score, precision_score, recall_score, f1_score,
        classification_report
    )
    import joblib
    ML_AVAILABLE = True
except ImportError:
    ML_AVAILABLE = False
    print("[WARN] ML dependencies not installed. Run: pip install xgboost scikit-learn pandas numpy joblib")


# 数据库路径
DB_PATH = os.path.join(os.path.dirname(os.path.abspath(__file__)), "stock.duckdb")
MODEL_DIR = os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "internal", "models")
os.makedirs(MODEL_DIR, exist_ok=True)


# ============================================================
# 数据库操作
# ============================================================

def explore_database(db_path: str) -> Dict:
    """探索数据库结构"""
    if not DUCKDB_AVAILABLE:
        return {"error": "duckdb not installed"}

    conn = duckdb.connect(db_path, read_only=True)

    # 获取所有表
    tables = conn.execute("""
        SELECT table_name 
        FROM information_schema.tables 
        WHERE table_schema = 'main'
        ORDER BY table_name
    """).fetchall()

    table_info = {}
    for table in tables:
        table_name = table[0]
        try:
            # 获取行数
            row_count = conn.execute(f"SELECT COUNT(*) FROM {table_name}").fetchone()[0]

            # 获取列信息
            columns = conn.execute(f"""
                SELECT column_name, data_type 
                FROM information_schema.columns 
                WHERE table_name = '{table_name}'
                ORDER BY ordinal_position
            """).fetchall()

            # 获取示例数据
            sample = conn.execute(f"SELECT * FROM {table_name} LIMIT 3").fetchdf()

            table_info[table_name] = {
                "row_count": row_count,
                "columns": [{"name": c[0], "type": c[1]} for c in columns],
                "sample": sample.to_dict(orient='records') if len(sample) > 0 else [],
            }
            print(f"  Table: {table_name}")
            print(f"    Rows: {row_count:,}")
            print(f"    Columns: {len(columns)}")
            if columns:
                print(f"    First column: {columns[0][0]} ({columns[0][1]})")
        except Exception as e:
            print(f"  Table: {table_name} - Error: {e}")

    conn.close()
    return {"tables": table_info}


def extract_kline_data(
    db_path: str,
    table_name: str = None,
    code_column: str = None,
    date_column: str = None,
    stock_code: str = None,
    limit: int = None
) -> pd.DataFrame:
    """
    从DuckDB提取K线数据
    
    Args:
        db_path: 数据库路径
        table_name: 表名（可选，自动检测）
        code_column: 股票代码列名（可选，自动检测）
        date_column: 日期列名（可选，自动检测）
        stock_code: 指定股票代码（可选，None=提取所有）
        limit: 限制行数（可选）
    """
    if not DUCKDB_AVAILABLE:
        raise ImportError("duckdb not installed")

    conn = duckdb.connect(db_path, read_only=True)

    # 自动检测表名和列名
    if table_name is None:
        # 查找包含K线数据的表
        tables = conn.execute("""
            SELECT table_name 
            FROM information_schema.tables 
            WHERE table_schema = 'main'
        """).fetchall()

        for table in tables:
            tname = table[0]
            try:
                # 检查表是否包含 open, high, low, close, volume 列
                cols = [c[0] for c in conn.execute(f"""
                    SELECT column_name 
                    FROM information_schema.columns 
                    WHERE table_name = '{tname}'
                """).fetchall()]

                if all(c in cols for c in ['open', 'high', 'low', 'close']):
                    table_name = tname
                    # 检测股票代码列
                    for col in cols:
                        if col.lower() in ['code', 'stock_code', 'symbol', 'ts_code']:
                            code_column = col
                            break
                    # 检测日期列
                    for col in cols:
                        if col.lower() in ['date', 'trade_date', 'datetime', 'trade_date']:
                            date_column = col
                            break
                    print(f"  Auto-detected table: {table_name}")
                    print(f"  Code column: {code_column}")
                    print(f"  Date column: {date_column}")
                    break
            except Exception:
                continue

    if table_name is None:
        raise ValueError("Could not auto-detect table with K-line data")

    # 构建查询
    query = f"SELECT * FROM {table_name}"
    conditions = []

    if stock_code and code_column:
        # 支持多种代码格式
        stock_code_formats = [stock_code]
        # 如果是6位纯数字，尝试添加市场前缀
        if stock_code.isdigit() and len(stock_code) == 6:
            if stock_code.startswith(('6', '9')):
                stock_code_formats.extend([f'SH{stock_code}', f'{stock_code}.SH'])
            elif stock_code.startswith(('0', '3')):
                stock_code_formats.extend([f'SZ{stock_code}', f'{stock_code}.SZ'])
            elif stock_code.startswith(('4', '8')):
                stock_code_formats.extend([f'BJ{stock_code}', f'{stock_code}.BJ'])

        if code_column:
            formats_str = "', '".join(stock_code_formats)
            conditions.append(f"UPPER({code_column}) IN ('{formats_str}')")

    if conditions:
        query += " WHERE " + " OR ".join(conditions)

    if date_column:
        query += f" ORDER BY {date_column}"

    if limit:
        query += f" LIMIT {limit}"

    print(f"\n  Executing query: {query[:200]}...")
    df = conn.execute(query).fetchdf()
    conn.close()

    print(f"  Extracted {len(df)} records")
    return df


def get_all_stock_codes(db_path: str, table_name: str = None, code_column: str = None) -> List[str]:
    """获取数据库中所有股票代码"""
    if not DUCKDB_AVAILABLE:
        return []

    conn = duckdb.connect(db_path, read_only=True)

    if table_name is None:
        # 自动检测
        tables = conn.execute("""
            SELECT table_name FROM information_schema.tables WHERE table_schema = 'main'
        """).fetchall()

        for table in tables:
            tname = table[0]
            try:
                cols = [c[0] for c in conn.execute(f"""
                    SELECT column_name FROM information_schema.columns WHERE table_name = '{tname}'
                """).fetchall()]
                if all(c in cols for c in ['open', 'high', 'low', 'close']):
                    table_name = tname
                    for col in cols:
                        if col.lower() in ['code', 'stock_code', 'symbol', 'ts_code']:
                            code_column = col
                            break
                    break
            except Exception:
                continue

    if table_name is None or code_column is None:
        conn.close()
        return []

    query = f"SELECT DISTINCT {code_column} FROM {table_name}"
    codes = conn.execute(query).fetchall()
    conn.close()

    return [c[0] for c in codes]


# ============================================================
# 特征工程
# ============================================================

class FeatureEngineer:
    """特征工程"""

    @staticmethod
    def calculate_features(df: pd.DataFrame) -> pd.DataFrame:
        """计算技术指标特征"""
        data = df.copy()

        # 确保列名小写
        cols_to_lower = {c: c.lower() for c in data.columns}
        data = data.rename(columns=cols_to_lower)

        # 1. 基础价格特征
        data['return_1d'] = data['close'].pct_change(1)
        data['return_5d'] = data['close'].pct_change(5)
        data['return_10d'] = data['close'].pct_change(10)
        data['return_20d'] = data['close'].pct_change(20)

        # 2. 波动率特征
        data['volatility_5'] = data['return_1d'].rolling(5).std()
        data['volatility_10'] = data['return_1d'].rolling(10).std()
        data['volatility_20'] = data['return_1d'].rolling(20).std()

        # 3. 均线特征
        for period in [5, 10, 20, 60]:
            data[f'ma_{period}'] = data['close'].rolling(period).mean()
            data[f'ma_ratio_{period}'] = data['close'] / data[f'ma_{period}'] - 1

        # 4. 均线多头/空头排列
        data['ma_alignment'] = (
            (data['ma_5'] > data['ma_10']).astype(int) +
            (data['ma_10'] > data['ma_20']).astype(int) +
            (data['ma_20'] > data['ma_60']).astype(int)
        )

        # 5. RSI 特征
        data['rsi_6'] = FeatureEngineer._calculate_rsi(data['close'], 6)
        data['rsi_14'] = FeatureEngineer._calculate_rsi(data['close'], 14)

        # 6. MACD 特征
        ema12 = data['close'].ewm(span=12, adjust=False).mean()
        ema26 = data['close'].ewm(span=26, adjust=False).mean()
        data['macd_dif'] = ema12 - ema26
        data['macd_dea'] = data['macd_dif'].ewm(span=9, adjust=False).mean()
        data['macd_hist'] = 2 * (data['macd_dif'] - data['macd_dea'])
        data['macd_cross'] = np.where(
            (data['macd_dif'] > data['macd_dea']) & (data['macd_dif'].shift(1) <= data['macd_dea'].shift(1)),
            1,
            np.where(
                (data['macd_dif'] < data['macd_dea']) & (data['macd_dif'].shift(1) >= data['macd_dea'].shift(1)),
                -1, 0
            )
        )

        # 7. KDJ 特征
        low_min = data['low'].rolling(9).min()
        high_max = data['high'].rolling(9).max()
        rsv = (data['close'] - low_min) / (high_max - low_min + 1e-10) * 100
        data['kdj_k'] = rsv.ewm(com=2, adjust=False).mean()
        data['kdj_d'] = data['kdj_k'].ewm(com=2, adjust=False).mean()
        data['kdj_j'] = 3 * data['kdj_k'] - 2 * data['kdj_d']

        # 8. 布林带特征
        data['boll_mid'] = data['close'].rolling(20).mean()
        data['boll_std'] = data['close'].rolling(20).std()
        data['boll_upper'] = data['boll_mid'] + 2 * data['boll_std']
        data['boll_lower'] = data['boll_mid'] - 2 * data['boll_std']
        data['boll_position'] = (data['close'] - data['boll_lower']) / (data['boll_upper'] - data['boll_lower'] + 1e-10)

        # 9. 成交量特征
        data['vol_ma_5'] = data['volume'].rolling(5).mean()
        data['vol_ma_10'] = data['volume'].rolling(10).mean()
        data['vol_ma_20'] = data['volume'].rolling(20).mean()
        data['vol_ratio'] = data['volume'] / data['vol_ma_20']
        data['vol_surge'] = np.where(data['volume'] > 2 * data['vol_ma_20'], 1, 0)

        # 10. 价格形态特征
        data['body_ratio'] = (data['close'] - data['open']) / (data['high'] - data['low'] + 1e-10)
        data['upper_shadow'] = (data['high'] - np.maximum(data['open'], data['close'])) / (data['high'] - data['low'] + 1e-10)
        data['lower_shadow'] = (np.minimum(data['open'], data['close']) - data['low']) / (data['high'] - data['low'] + 1e-10)
        data['is_yang'] = (data['close'] > data['open']).astype(int)

        # 11. 动量特征
        data['momentum_10'] = data['close'] / data['close'].shift(10) - 1
        data['momentum_20'] = data['close'] / data['close'].shift(20) - 1

        # 12. 新高/新低特征
        data['high_20d'] = data['high'].rolling(20).max()
        data['low_20d'] = data['low'].rolling(20).min()
        data['new_high'] = (data['close'] >= data['high_20d']).astype(int)
        data['new_low'] = (data['close'] <= data['low_20d']).astype(int)

        return data

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
        """创建训练标签"""
        future_return = df['close'].shift(-forward_days) / df['close'] - 1
        labels = pd.Series(0, index=df.index)
        labels[future_return > threshold] = 1
        labels[future_return < -threshold] = -1
        return labels


# ============================================================
# XGBoost 模型训练
# ============================================================

def train_xgboost_model(
    df: pd.DataFrame,
    model_name: str = "xgboost_astock",
    forward_days: int = 5,
    threshold: float = 0.02,
    test_size: float = 0.2,
    params: Dict = None
) -> Dict:
    """
    训练XGBoost模型
    
    Args:
        df: K线数据DataFrame (需要包含 open, high, low, close, volume)
        model_name: 模型名称
        forward_days: 预测未来多少天
        threshold: 涨跌幅阈值
        test_size: 测试集比例
        params: XGBoost 参数
    """
    if not ML_AVAILABLE:
        return {"error": "ML dependencies not installed"}

    # 特征工程
    print("\n  Calculating features...")
    engine = FeatureEngineer()
    df_features = engine.calculate_features(df)
    df_features = df_features.dropna()
    
    # 排除非数值列（如symbol等字符串）
    numeric_cols = df_features.select_dtypes(include=[np.number]).columns.tolist()
    df_features = df_features[numeric_cols]
    print(f"  Features calculated: {len(df_features)} rows x {len(df_features.columns)} columns (numeric only)")

    # 创建标签
    print("  Creating labels...")
    labels = engine.create_labels(df, forward_days, threshold)
    valid_idx = df_features.index.intersection(labels.dropna().index)

    X = df_features.loc[valid_idx]
    # 将标签从 {-1, 0, 1} 转换为 {0, 1, 2} (XGBoost要求)
    y = labels.loc[valid_idx].astype(int) + 1  # -1→0, 0→1, 1→2

    print(f"  Valid samples: {len(X)}")
    print(f"  Label distribution: SELL={int((y == 0).sum())}, HOLD={int((y == 1).sum())}, BUY={int((y == 2).sum())}")

    # 处理特征列名
    feature_cols = X.columns.tolist()
    print(f"  Feature columns ({len(feature_cols)}): {feature_cols[:10]}...")

    # 数据划分
    print("  Splitting data...")
    X_train, X_test, y_train, y_test = train_test_split(
        X, y, test_size=test_size, random_state=42, stratify=y
    )

    # 特征标准化
    print("  Scaling features...")
    scaler = StandardScaler()
    X_train_scaled = scaler.fit_transform(X_train)
    X_test_scaled = scaler.transform(X_test)

    # 默认参数
    default_params = {
        'max_depth': 6,
        'learning_rate': 0.1,
        'n_estimators': 200,
        'min_child_weight': 3,
        'subsample': 0.8,
        'colsample_bytree': 0.8,
        'reg_alpha': 0.1,
        'reg_lambda': 1.0,
        'objective': 'multi:softmax',
        'num_class': 3,
        'eval_metric': 'mlogloss',
        'random_state': 42,
        'use_label_encoder': False,
    }

    if params:
        default_params.update(params)

    # 训练模型
    print("  Training XGBoost...")
    model = xgb.XGBClassifier(**default_params)
    model.fit(
        X_train_scaled, y_train,
        eval_set=[(X_test_scaled, y_test)],
        verbose=50
    )

    # 预测
    y_pred_train = model.predict(X_train_scaled)
    y_pred_test = model.predict(X_test_scaled)

    # 评估
    results = {
        'model_name': model_name,
        'created_at': time.strftime('%Y-%m-%d %H:%M:%S'),
        'params': default_params,
        'feature_columns': feature_cols,
        'forward_days': forward_days,
        'threshold': threshold,
        'train_accuracy': float(round(accuracy_score(y_train, y_pred_train), 4)),
        'test_accuracy': float(round(accuracy_score(y_test, y_pred_test), 4)),
        'test_precision_macro': float(round(precision_score(y_test, y_pred_test, average='macro', zero_division=0), 4)),
        'test_recall_macro': float(round(recall_score(y_test, y_pred_test, average='macro', zero_division=0), 4)),
        'test_f1_macro': float(round(f1_score(y_test, y_pred_test, average='macro', zero_division=0), 4)),
        'train_samples': len(y_train),
        'test_samples': len(y_test),
        'total_samples': len(X),
        'label_distribution': {
            'sell': int((y == 0).sum()),
            'hold': int((y == 1).sum()),
            'buy': int((y == 2).sum()),
        }
    }

    # 交叉验证（采样数据做CV，避免耗时过长）
    if len(X) >= 500:
        print("  Running cross-validation (sampled)...")
        try:
            # 使用最多10万条数据做交叉验证
            if len(X) > 100000:
                idx = np.random.choice(len(X), 100000, replace=False)
                X_cv = X.iloc[idx]
                y_cv = y.iloc[idx]
            else:
                X_cv = X
                y_cv = y
            
            cv_scores = cross_val_score(
                xgb.XGBClassifier(**default_params),
                scaler.transform(X_cv), y_cv,
                cv=5, scoring='accuracy', verbose=False, n_jobs=-1
            )
            results['cv_accuracy_mean'] = float(round(cv_scores.mean(), 4))
            results['cv_accuracy_std'] = float(round(cv_scores.std(), 4))
        except Exception as e:
            print(f"    CV failed: {e}")

    # 特征重要性
    if hasattr(model, 'feature_importances_'):
        importance = model.feature_importances_
        top_idx = np.argsort(importance)[::-1][:20]
        results['top_features'] = [
            {'feature': feature_cols[i], 'importance': float(round(importance[i], 4))}
            for i in top_idx
        ]

    # 保存模型
    model_path = os.path.join(MODEL_DIR, f"{model_name}.json")
    scaler_path = os.path.join(MODEL_DIR, f"{model_name}_scaler.pkl")
    info_path = os.path.join(MODEL_DIR, f"{model_name}_info.json")

    # 保存特征列信息，用于后续预测
    results['feature_columns'] = feature_cols
    results['model_name'] = model_name

    model.save_model(model_path)
    joblib.dump(scaler, scaler_path)

    with open(info_path, 'w', encoding='utf-8') as f:
        json.dump(results, f, ensure_ascii=False, indent=2)

    print(f"\n  Model saved:")
    print(f"    Model: {model_path}")
    print(f"    Scaler: {scaler_path}")
    print(f"    Info: {info_path}")
    print(f"    Features: {len(feature_cols)} columns")

    return results


def main():
    print("=" * 60)
    print("DuckDB → XGBoost Training Script")
    print("=" * 60)

    # 检查依赖
    if not DUCKDB_AVAILABLE:
        print("\n[ERROR] duckdb not installed!")
        print("  Install: pip install duckdb")
        return

    if not ML_AVAILABLE:
        print("\n[ERROR] ML dependencies not installed!")
        print("  Install: pip install -r ml_requirements.txt")
        return

    # 检查数据库
    if not os.path.exists(DB_PATH):
        print(f"\n[ERROR] Database not found: {DB_PATH}")
        return

    print(f"\nDatabase: {DB_PATH}")

    # 1. 探索数据库
    print("\n" + "-" * 40)
    print("Step 1: Exploring database structure...")
    print("-" * 40)

    db_info = explore_database(DB_PATH)
    if 'error' in db_info:
        print(f"  Error: {db_info['error']}")
        return

    # 2. 提取数据
    print("\n" + "-" * 40)
    print("Step 2: Extracting K-line data...")
    print("-" * 40)

    # 先获取股票代码列表
    print("\n  Getting stock list...")
    all_codes = get_all_stock_codes(DB_PATH)
    print(f"  Total stocks in database: {len(all_codes)}")

    if not all_codes:
        print("  [ERROR] No stocks found in database")
        return

    # 采样选择500只股票（有足够历史数据的）
    # 先获取股票数据量信息
    print("  Analyzing stock data distribution...")
    conn = duckdb.connect(DB_PATH, read_only=True)
    stock_counts = conn.execute("""
        SELECT symbol, COUNT(*) as cnt 
        FROM ohlc 
        GROUP BY symbol 
        HAVING COUNT(*) >= 120
        ORDER BY cnt DESC
        LIMIT 500
    """).fetchall()
    conn.close()
    
    sample_stocks = [row[0] for row in stock_counts]
    print(f"  Selected {len(sample_stocks)} stocks with >= 120 records")

    # 提取采样股票的数据
    print(f"\n  Extracting data for {len(sample_stocks)} stocks...")
    try:
        # 构建股票代码过滤条件
        codes_str = "', '".join(sample_stocks)
        conn = duckdb.connect(DB_PATH, read_only=True)
        query = f"""
            SELECT * FROM ohlc 
            WHERE symbol IN ('{codes_str}')
            ORDER BY symbol, date
        """
        full_df = conn.execute(query).fetchdf()
        conn.close()
        print(f"  Total records: {len(full_df):,}")

        if len(full_df) > 0:
            # 确保列名小写
            full_df.columns = [c.lower() for c in full_df.columns]

            # 检查必要列
            required_cols = ['open', 'high', 'low', 'close', 'volume']
            missing_cols = [c for c in required_cols if c not in full_df.columns]
            if missing_cols:
                print(f"  [WARN] Missing columns: {missing_cols}")
                print(f"  Available columns: {list(full_df.columns)}")
                # 尝试映射
                col_mapping = {
                    'vol': 'volume',
                    'trade_date': 'date',
                    'ts_code': 'code',
                }
                for old, new in col_mapping.items():
                    if old in full_df.columns and new not in full_df.columns:
                        full_df[new] = full_df[old]

            print(f"  Columns: {list(full_df.columns)}")
            print(f"  Date range: {full_df['date'].min()} to {full_df['date'].max()}")

            # 3. 训练模型
            print("\n" + "-" * 40)
            print("Step 3: Training XGBoost model...")
            print("-" * 40)

            # 准备训练数据
            train_df = full_df.copy()
            # 确保有 'code' 列（从 'symbol' 列映射）
            if 'symbol' in train_df.columns and 'code' not in train_df.columns:
                train_df['code'] = train_df['symbol']

            print(f"  Training data: {len(train_df):,} rows")

            # 训练
            results = train_xgboost_model(
                train_df,
                model_name="xgboost_astock_v1",
                forward_days=5,
                threshold=0.02,
                test_size=0.2,
                params={
                    'max_depth': 6,
                    'learning_rate': 0.1,
                    'n_estimators': 200,
                    'min_child_weight': 3,
                    'subsample': 0.8,
                    'colsample_bytree': 0.8,
                }
            )

            # 打印结果
            print("\n" + "-" * 40)
            print("Training Results:")
            print("-" * 40)
            print(f"  Train Accuracy: {results.get('train_accuracy', 0):.4f}")
            print(f"  Test Accuracy: {results.get('test_accuracy', 0):.4f}")
            print(f"  Precision (macro): {results.get('test_precision_macro', 0):.4f}")
            print(f"  Recall (macro): {results.get('test_recall_macro', 0):.4f}")
            print(f"  F1 (macro): {results.get('test_f1_macro', 0):.4f}")
            if results.get('cv_accuracy_mean'):
                print(f"  CV Accuracy: {results['cv_accuracy_mean']:.4f} ± {results['cv_accuracy_std']:.4f}")
            print(f"  Total Samples: {results.get('total_samples', 0):,}")
            print(f"  Features: {len(results.get('feature_columns', []))}")
            print(f"  Label Distribution: {results.get('label_distribution', {})}")

            if results.get('top_features'):
                print("\n  Top 20 Features:")
                for i, feat in enumerate(results['top_features'], 1):
                    print(f"    {i:2d}. {feat['feature']:25s}: {feat['importance']:.4f}")

    except Exception as e:
        import traceback
        print(f"\n  [ERROR] {e}")
        traceback.print_exc()

    print("\n" + "=" * 60)
    print("Done!")
    print("=" * 60)


if __name__ == '__main__':
    main()
