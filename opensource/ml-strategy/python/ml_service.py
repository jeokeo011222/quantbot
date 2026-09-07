"""
QuantBot 机器学习策略服务
基于 XGBoost 的量化选股策略，提供 HTTP API 接口

功能：
1. 特征工程：从K线数据提取技术指标特征
2. 模型训练：使用XGBoost训练分类模型
3. 信号预测：基于训练好的模型生成买卖信号
4. 模型管理：保存/加载/评估模型

依赖：
    pip install xgboost scikit-learn pandas numpy flask flask-cors joblib

使用：
    python ml_service.py
    默认监听 http://127.0.0.1:8766

API 端点：
    GET /health                              健康检查
    POST /api/train                          训练模型
    POST /api/predict                        预测信号
    POST /api/backtest                       回测
    GET /api/models                          获取模型列表
    GET /api/model/{name}                    获取模型详情
    DELETE /api/model/{name}                 删除模型
    POST /api/model/{name}/evaluate          评估模型
"""

import json
import os
import sys
import time
import warnings
from typing import Dict, List, Optional, Tuple

import numpy as np
import pandas as pd
from flask import Flask, jsonify, request
from flask_cors import CORS

# XGBoost 相关
try:
    import xgboost as xgb
    from sklearn.model_selection import train_test_split, cross_val_score
    from sklearn.preprocessing import StandardScaler
    from sklearn.metrics import (
        accuracy_score, precision_score, recall_score, f1_score,
        roc_auc_score, confusion_matrix, classification_report
    )
    import joblib
    ML_AVAILABLE = True
except ImportError:
    ML_AVAILABLE = False
    print("[WARN] ML dependencies not installed. Run: pip install xgboost scikit-learn pandas numpy joblib")

warnings.filterwarnings('ignore')

app = Flask(__name__)
CORS(app)

# 模型存储目录
# 优先从环境变量 QUANTPILOT_MODEL_DIR 指定；否则按随程序分发的常见位置查找
# （脚本与 models 同目录、上级 models），最后回退到开发目录 internal/models。
def _resolve_model_dir():
    env = os.environ.get("QUANTPILOT_MODEL_DIR")
    if env:
        return env
    base = os.path.dirname(os.path.abspath(__file__))
    candidates = [
        os.path.join(base, "models"),          # build/bin/models（脚本与模型同目录分发）
        os.path.join(base, "..", "models"),    # build/bin/python -> build/bin/models
        os.path.join(base, "data", "models"),  # build/bin/data/models
        os.path.join(base, "..", "internal", "models"),  # 开发目录回退
    ]
    for p in candidates:
        if os.path.isdir(os.path.normpath(p)):
            return os.path.normpath(p)
    return os.path.normpath(candidates[0])


MODEL_DIR = _resolve_model_dir()
os.makedirs(MODEL_DIR, exist_ok=True)


# ============================================================
# 特征工程模块
# ============================================================

class FeatureEngineer:
    """特征工程：从K线数据提取技术指标特征"""

    @staticmethod
    def calculate_features(df: pd.DataFrame) -> pd.DataFrame:
        """
        计算技术指标特征
        
        Args:
            df: 包含 open, high, low, close, volume 列的DataFrame
            
        Returns:
            添加了特征列的DataFrame
        """
        data = df.copy()

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

        # 删除原始列中不需要的（保留close用于后续标签计算）
        feature_cols = [c for c in data.columns if c not in ['open', 'high', 'low', 'volume']]

        return data

    @staticmethod
    def _calculate_rsi(prices: pd.Series, period: int) -> pd.Series:
        """计算RSI指标"""
        delta = prices.diff()
        gain = (delta.where(delta > 0, 0)).rolling(window=period).mean()
        loss = (-delta.where(delta < 0, 0)).rolling(window=period).mean()
        rs = gain / (loss + 1e-10)
        rsi = 100 - (100 / (1 + rs))
        return rsi

    @staticmethod
    def create_labels(df: pd.DataFrame, forward_days: int = 5, threshold: float = 0.02) -> pd.Series:
        """
        创建训练标签
        
        Args:
            df: 包含close价格的DataFrame
            forward_days: 预测未来多少天
            threshold: 涨跌幅阈值
            
        Returns:
            标签序列 (1=买入, 0=观望, -1=卖出)
        """
        future_return = df['close'].shift(-forward_days) / df['close'] - 1

        labels = pd.Series(0, index=df.index)
        labels[future_return > threshold] = 1
        labels[future_return < -threshold] = -1

        return labels


# ============================================================
# XGBoost 模型管理器
# ============================================================

class XGBoostModel:
    """XGBoost 分类模型封装"""

    def __init__(self, model_name: str = "xgboost_default"):
        self.model_name = model_name
        self.model: Optional[xgb.XGBClassifier] = None
        self.scaler: Optional[StandardScaler] = None
        self.feature_columns: List[str] = []
        self.model_info: Dict = {}

        # XGBoost 默认参数
        self.params = {
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
            'use_label_encoder': False,
            'random_state': 42,
        }

    def train(
        self,
        X: pd.DataFrame,
        y: pd.Series,
        test_size: float = 0.2,
        cv_folds: int = 5
    ) -> Dict:
        """
        训练XGBoost模型
        
        Args:
            X: 特征DataFrame
            y: 标签序列
            test_size: 测试集比例
            cv_folds: 交叉验证折数
            
        Returns:
            训练结果信息
        """
        self.feature_columns = X.columns.tolist()

        # 将标签从 {-1,0,1} 转换为 {0,1,2} (XGBoost要求)
        y_xgb = y + 1  # -1→0, 0→1, 1→2

        # 数据划分
        X_train, X_test, y_train, y_test = train_test_split(
            X, y_xgb, test_size=test_size, random_state=42, stratify=y_xgb
        )

        # 特征标准化
        self.scaler = StandardScaler()
        X_train_scaled = self.scaler.fit_transform(X_train)
        X_test_scaled = self.scaler.transform(X_test)

        # 训练模型
        self.model = xgb.XGBClassifier(**self.params)
        self.model.fit(
            X_train_scaled, y_train,
            eval_set=[(X_test_scaled, y_test)],
            verbose=False
        )

        # 预测
        y_pred_train = self.model.predict(X_train_scaled)
        y_pred_test = self.model.predict(X_test_scaled)

        # 评估指标
        results = {
            'train_accuracy': accuracy_score(y_train, y_pred_train),
            'test_accuracy': accuracy_score(y_test, y_pred_test),
            'test_precision_macro': precision_score(y_test, y_pred_test, average='macro', zero_division=0),
            'test_recall_macro': recall_score(y_test, y_pred_test, average='macro', zero_division=0),
            'test_f1_macro': f1_score(y_test, y_pred_test, average='macro', zero_division=0),
            'predictions_count': len(y_pred_test),
            'train_samples': len(y_train),
            'test_samples': len(y_test),
            'label_distribution': {
                'sell': int((y_xgb == 0).sum()),
                'hold': int((y_xgb == 1).sum()),
                'buy': int((y_xgb == 2).sum()),
            }
        }

        # 交叉验证
        if len(X) >= 100:
            try:
                cv_scores = cross_val_score(
                    xgb.XGBClassifier(**self.params),
                    self.scaler.transform(X), y_xgb,
                    cv=cv_folds, scoring='accuracy'
                )
                results['cv_accuracy_mean'] = float(cv_scores.mean())
                results['cv_accuracy_std'] = float(cv_scores.std())
            except Exception as e:
                results['cv_error'] = str(e)

        # 特征重要性
        if self.model is not None:
            importance = self.model.feature_importances_
            top_features = sorted(
                zip(self.feature_columns, importance),
                key=lambda x: x[1],
                reverse=True
            )[:10]
            results['top_features'] = [
                {'feature': f, 'importance': float(round(imp, 4))}
                for f, imp in top_features
            ]

        # 保存模型信息
        self.model_info = {
            'model_name': self.model_name,
            'created_at': time.strftime('%Y-%m-%d %H:%M:%S'),
            'params': self.params,
            'results': results,
        }

        return results

    def predict(self, X: pd.DataFrame) -> np.ndarray:
        """预测，将输出从 {0,1,2} 转换为 {-1,0,1}"""
        if self.model is None or self.scaler is None:
            raise ValueError("Model not loaded. Please train or load a model first.")

        X_scaled = self.scaler.transform(X[self.feature_columns])
        raw_predictions = self.model.predict(X_scaled)
        # 将 {0,1,2} 转换为 {-1,0,1}: 0→-1(SELL), 1→0(HOLD), 2→1(BUY)
        return raw_predictions - 1

    def predict_proba(self, X: pd.DataFrame) -> np.ndarray:
        """预测概率"""
        if self.model is None or self.scaler is None:
            raise ValueError("Model not loaded. Please train or load a model first.")

        X_scaled = self.scaler.transform(X[self.feature_columns])
        return self.model.predict_proba(X_scaled)

    def save(self) -> str:
        """保存模型"""
        if self.model is None:
            raise ValueError("No model to save.")

        model_path = os.path.join(MODEL_DIR, f"{self.model_name}.json")
        scaler_path = os.path.join(MODEL_DIR, f"{self.model_name}_scaler.pkl")
        info_path = os.path.join(MODEL_DIR, f"{self.model_name}_info.json")

        self.model.save_model(model_path)
        joblib.dump(self.scaler, scaler_path)

        # 保存模型信息
        with open(info_path, 'w', encoding='utf-8') as f:
            json.dump(self.model_info, f, ensure_ascii=False, indent=2)

        return model_path

    def load(self) -> bool:
        """加载模型"""
        model_path = os.path.join(MODEL_DIR, f"{self.model_name}.json")
        scaler_path = os.path.join(MODEL_DIR, f"{self.model_name}_scaler.pkl")
        info_path = os.path.join(MODEL_DIR, f"{self.model_name}_info.json")

        if not os.path.exists(model_path):
            return False

        self.model = xgb.XGBClassifier()
        self.model.load_model(model_path)
        self.scaler = joblib.load(scaler_path)

        with open(info_path, 'r', encoding='utf-8') as f:
            self.model_info = json.load(f)
            self.feature_columns = self.model_info.get('feature_columns', [])

        # 如果 feature_columns 为空，从模型信息中获取
        if not self.feature_columns and 'results' in self.model_info:
            self.feature_columns = []

        return True

    @staticmethod
    def list_models() -> List[Dict]:
        """列出所有可用模型"""
        models = []
        for filename in os.listdir(MODEL_DIR):
            if filename.endswith('_info.json'):
                model_name = filename.replace('_info.json', '')
                info_path = os.path.join(MODEL_DIR, filename)
                try:
                    with open(info_path, 'r', encoding='utf-8') as f:
                        info = json.load(f)
                        models.append({
                            'name': model_name,
                            'created_at': info.get('created_at', 'unknown'),
                            'test_accuracy': info.get('test_accuracy', info.get('results', {}).get('test_accuracy', 0)),
                            'params': info.get('params', {}),
                        })
                except Exception:
                    pass
        return models

    @staticmethod
    def delete_model(model_name: str) -> bool:
        """删除模型"""
        for suffix in ['.json', '_scaler.pkl', '_info.json']:
            path = os.path.join(MODEL_DIR, f"{model_name}{suffix}")
            if os.path.exists(path):
                os.remove(path)
        return True


# ============================================================
# 回测引擎
# ============================================================

class BacktestEngine:
    """简单的回测引擎"""

    @staticmethod
    def run(
        df: pd.DataFrame,
        signals: np.ndarray,
        initial_capital: float = 100000.0,
        commission_rate: float = 0.0003,
        slippage: float = 0.001
    ) -> Dict:
        """
        运行回测
        
        Args:
            df: K线数据DataFrame
            signals: 信号数组 (-1=卖出, 0=观望, 1=买入)
            initial_capital: 初始资金
            commission_rate: 手续费率
            slippage: 滑点
            
        Returns:
            回测结果
        """
        capital = initial_capital
        position = 0
        trades = []
        equity_curve = [initial_capital]
        peak_capital = initial_capital
        max_drawdown = 0

        for i in range(len(df)):
            price = df['close'].iloc[i]
            signal = signals[i] if i < len(signals) else 0

            # 买入
            if signal == 1 and position == 0 and price > 0:
                shares = int(capital / (price * (1 + slippage)) / 100) * 100
                if shares > 0:
                    cost = shares * price * (1 + slippage) * (1 + commission_rate)
                    capital -= cost
                    position = shares
                    trades.append({
                        'type': 'BUY',
                        'price': price,
                        'shares': shares,
                        'cost': cost,
                        'date': i
                    })

            # 卖出
            elif signal == -1 and position > 0 and price > 0:
                revenue = position * price * (1 - slippage) * (1 - commission_rate)
                capital += revenue
                trades.append({
                    'type': 'SELL',
                    'price': price,
                    'shares': position,
                    'revenue': revenue,
                    'date': i,
                    'pnl': revenue - (trades[-1]['cost'] if trades and trades[-1]['type'] == 'BUY' else 0)
                })
                position = 0

            # 计算当前权益
            current_equity = capital + position * price
            equity_curve.append(current_equity)

            # 计算回撤
            if current_equity > peak_capital:
                peak_capital = current_equity
            drawdown = (peak_capital - current_equity) / peak_capital
            if drawdown > max_drawdown:
                max_drawdown = drawdown

        # 计算回测指标
        final_capital = capital + position * df['close'].iloc[-1] if len(df) > 0 else initial_capital
        total_return = (final_capital / initial_capital - 1) * 100
        n_trades = len([t for t in trades if t['type'] == 'BUY'])

        # 计算夏普比率
        equity_series = pd.Series(equity_curve)
        returns = equity_series.pct_change().dropna()
        sharpe_ratio = returns.mean() / (returns.std() + 1e-10) * np.sqrt(252)

        return {
            'initial_capital': initial_capital,
            'final_capital': float(round(final_capital, 2)),
            'total_return_pct': float(round(total_return, 2)),
            'max_drawdown_pct': float(round(max_drawdown * 100, 2)),
            'sharpe_ratio': float(round(sharpe_ratio, 4)),
            'total_trades': n_trades,
            'trades': trades[:50],  # 只返回前50条
            'equity_curve': [float(round(x, 2)) for x in equity_curve[-100:]],  # 只返回最近100个点
        }


# ============================================================
# HTTP API 端点
# ============================================================

@app.route('/health', methods=['GET'])
def health_check():
    """健康检查"""
    return jsonify({
        'status': 'ok',
        'ml_available': ML_AVAILABLE,
        'model_dir': MODEL_DIR,
        'models_count': len([f for f in os.listdir(MODEL_DIR) if f.endswith('.json')]) if os.path.exists(MODEL_DIR) else 0,
    })


@app.route('/api/train', methods=['POST'])
def train_model():
    """
    训练XGBoost模型
    
    请求体:
        {
            "model_name": "xgboost_v1",
            "data": [
                {"date": "2024-01-02", "open": 10.5, "high": 11.2, "low": 10.3, "close": 11.0, "volume": 1000000},
                ...
            ],
            "forward_days": 5,
            "threshold": 0.02,
            "test_size": 0.2,
            "params": {"max_depth": 8, "learning_rate": 0.05, ...}
        }
    """
    if not ML_AVAILABLE:
        return jsonify({'error': 'ML dependencies not installed'}), 500

    try:
        req = request.get_json()
        model_name = req.get('model_name', 'xgboost_default')
        data = req.get('data', [])
        forward_days = req.get('forward_days', 5)
        threshold = req.get('threshold', 0.02)
        test_size = req.get('test_size', 0.2)
        custom_params = req.get('params', {})

        if len(data) < 60:
            return jsonify({'error': f'Insufficient data: need at least 60 records, got {len(data)}'}), 400

        # 数据准备
        df = pd.DataFrame(data)
        required_cols = ['open', 'high', 'low', 'close', 'volume']
        for col in required_cols:
            if col not in df.columns:
                return jsonify({'error': f'Missing required column: {col}'}), 400

        # 特征工程
        engine = FeatureEngineer()
        df_features = engine.calculate_features(df)
        df_features = df_features.dropna()

        if len(df_features) < 30:
            return jsonify({'error': f'Insufficient features after processing: {len(df_features)} rows'}), 400

        # 创建标签
        labels = engine.create_labels(df, forward_days, threshold)
        valid_idx = df_features.index.intersection(labels.dropna().index)

        X = df_features.loc[valid_idx].drop(columns=['close'], errors='ignore')
        y = labels.loc[valid_idx].astype(int)

        if len(X) < 30:
            return jsonify({'error': f'Insufficient valid samples: {len(X)}'}), 400

        # 训练模型
        model = XGBoostModel(model_name)
        if custom_params:
            model.params.update(custom_params)

        results = model.train(X, y, test_size=test_size)

        # 保存模型
        model.model_info['feature_columns'] = X.columns.tolist()
        model.save()

        return jsonify({
            'status': 'success',
            'model_name': model_name,
            'results': results,
            'feature_count': len(X.columns),
            'samples_used': len(X),
        })

    except Exception as e:
        return jsonify({'error': str(e)}), 500


@app.route('/api/predict', methods=['POST'])
def predict_signals():
    """
    使用模型预测信号
    
    请求体:
        {
            "model_name": "xgboost_v1",
            "data": [
                {"date": "2024-01-02", "open": 10.5, "high": 11.2, "low": 10.3, "close": 11.0, "volume": 1000000},
                ...
            ],
            "return_proba": false
        }
    """
    if not ML_AVAILABLE:
        return jsonify({'error': 'ML dependencies not installed'}), 500

    try:
        req = request.get_json()
        model_name = req.get('model_name', 'xgboost_default')
        data = req.get('data', [])
        return_proba = req.get('return_proba', False)

        # 加载模型
        model = XGBoostModel(model_name)
        if not model.load():
            return jsonify({'error': f'Model not found: {model_name}'}), 404

        # 数据准备
        df = pd.DataFrame(data)
        required_cols = ['open', 'high', 'low', 'close', 'volume']
        for col in required_cols:
            if col not in df.columns:
                return jsonify({'error': f'Missing required column: {col}'}), 400

        # 特征工程
        engine = FeatureEngineer()
        df_features = engine.calculate_features(df)

        # 使用模型特征列
        feature_cols = model.feature_columns
        if not feature_cols:
            feature_cols = [c for c in df_features.columns if c != 'close']

        X = df_features[feature_cols].fillna(0)

        # 预测
        if return_proba:
            proba = model.predict_proba(X)
            predictions = np.argmax(proba, axis=1) - 1  # -1, 0, 1
            return jsonify({
                'status': 'success',
                'model_name': model_name,
                'signals': predictions.tolist(),
                'probabilities': proba.tolist(),
            })
        else:
            predictions = model.predict(X)
            return jsonify({
                'status': 'success',
                'model_name': model_name,
                'signals': predictions.tolist(),
            })

    except Exception as e:
        return jsonify({'error': str(e)}), 500


@app.route('/api/backtest', methods=['POST'])
def run_backtest():
    """
    使用模型进行回测
    
    请求体:
        {
            "model_name": "xgboost_v1",
            "data": [
                {"date": "2024-01-02", "open": 10.5, "high": 11.2, "low": 10.3, "close": 11.0, "volume": 1000000},
                ...
            ],
            "initial_capital": 100000,
            "commission_rate": 0.0003,
            "slippage": 0.001
        }
    """
    if not ML_AVAILABLE:
        return jsonify({'error': 'ML dependencies not installed'}), 500

    try:
        req = request.get_json()
        model_name = req.get('model_name', 'xgboost_default')
        data = req.get('data', [])
        initial_capital = req.get('initial_capital', 100000)
        commission_rate = req.get('commission_rate', 0.0003)
        slippage = req.get('slippage', 0.001)

        # 加载模型
        model = XGBoostModel(model_name)
        if not model.load():
            return jsonify({'error': f'Model not found: {model_name}'}), 404

        # 数据准备
        df = pd.DataFrame(data)
        required_cols = ['open', 'high', 'low', 'close', 'volume']
        for col in required_cols:
            if col not in df.columns:
                return jsonify({'error': f'Missing required column: {col}'}), 400

        # 特征工程
        engine = FeatureEngineer()
        df_features = engine.calculate_features(df)

        # 使用模型特征列
        feature_cols = model.feature_columns
        if not feature_cols:
            feature_cols = [c for c in df_features.columns if c != 'close']

        X = df_features[feature_cols].fillna(0)

        # 预测信号
        signals = model.predict(X)

        # 运行回测
        bt = BacktestEngine()
        results = bt.run(df, signals, initial_capital, commission_rate, slippage)

        return jsonify({
            'status': 'success',
            'model_name': model_name,
            'backtest': results,
        })

    except Exception as e:
        return jsonify({'error': str(e)}), 500


@app.route('/api/models', methods=['GET'])
def list_models():
    """获取模型列表"""
    if not ML_AVAILABLE:
        return jsonify({'error': 'ML dependencies not installed'}), 500

    try:
        models = XGBoostModel.list_models()
        return jsonify({
            'status': 'success',
            'models': models,
        })
    except Exception as e:
        return jsonify({'error': str(e)}), 500


@app.route('/api/model/<model_name>', methods=['GET'])
def get_model_info(model_name: str):
    """获取模型详情"""
    if not ML_AVAILABLE:
        return jsonify({'error': 'ML dependencies not installed'}), 500

    try:
        info_path = os.path.join(MODEL_DIR, f"{model_name}_info.json")
        if not os.path.exists(info_path):
            return jsonify({'error': f'Model not found: {model_name}'}), 404

        with open(info_path, 'r', encoding='utf-8') as f:
            info = json.load(f)

        return jsonify({
            'status': 'success',
            'model': info,
        })
    except Exception as e:
        return jsonify({'error': str(e)}), 500


@app.route('/api/model/<model_name>', methods=['DELETE'])
def delete_model(model_name: str):
    """删除模型"""
    if not ML_AVAILABLE:
        return jsonify({'error': 'ML dependencies not installed'}), 500

    try:
        XGBoostModel.delete_model(model_name)
        return jsonify({
            'status': 'success',
            'message': f'Model {model_name} deleted',
        })
    except Exception as e:
        return jsonify({'error': str(e)}), 500


@app.route('/api/model/<model_name>/evaluate', methods=['POST'])
def evaluate_model(model_name: str):
    """
    评估模型
    
    请求体:
        {
            "data": [...],
            "forward_days": 5,
            "threshold": 0.02
        }
    """
    if not ML_AVAILABLE:
        return jsonify({'error': 'ML dependencies not installed'}), 500

    try:
        req = request.get_json()
        data = req.get('data', [])
        forward_days = req.get('forward_days', 5)
        threshold = req.get('threshold', 0.02)

        # 加载模型
        model = XGBoostModel(model_name)
        if not model.load():
            return jsonify({'error': f'Model not found: {model_name}'}), 404

        # 数据准备
        df = pd.DataFrame(data)
        required_cols = ['open', 'high', 'low', 'close', 'volume']
        for col in required_cols:
            if col not in df.columns:
                return jsonify({'error': f'Missing required column: {col}'}), 400

        # 特征工程
        engine = FeatureEngineer()
        df_features = engine.calculate_features(df)

        # 创建标签
        labels = engine.create_labels(df, forward_days, threshold)
        valid_idx = df_features.index.intersection(labels.dropna().index)

        feature_cols = model.feature_columns
        if not feature_cols:
            feature_cols = [c for c in df_features.columns if c != 'close']

        X = df_features.loc[valid_idx][feature_cols].fillna(0)
        y_true = labels.loc[valid_idx].astype(int)

        # 预测
        y_pred = model.predict(X)

        # 评估
        evaluation = {
            'accuracy': float(round(accuracy_score(y_true, y_pred), 4)),
            'precision_macro': float(round(precision_score(y_true, y_pred, average='macro', zero_division=0), 4)),
            'recall_macro': float(round(recall_score(y_true, y_pred, average='macro', zero_division=0), 4)),
            'f1_macro': float(round(f1_score(y_true, y_pred, average='macro', zero_division=0), 4)),
            'report': classification_report(y_true, y_pred, output_dict=True, zero_division=0),
            'confusion_matrix': confusion_matrix(y_true, y_pred).tolist(),
            'samples': len(y_true),
        }

        return jsonify({
            'status': 'success',
            'model_name': model_name,
            'evaluation': evaluation,
        })

    except Exception as e:
        return jsonify({'error': str(e)}), 500


# ============================================================
# 启动服务
# ============================================================

if __name__ == '__main__':
    if not ML_AVAILABLE:
        print("[ERROR] ML dependencies not installed!")
        print("Please install required packages:")
        print("  pip install xgboost scikit-learn pandas numpy flask flask-cors joblib")
        sys.exit(1)

    print("=" * 60)
    print("QuantBot XGBoost ML Service")
    print("=" * 60)
    print(f"Model directory: {MODEL_DIR}")
    print(f"API endpoint: http://127.0.0.1:8766")
    print()
    print("API Routes:")
    print("  GET  /health                  - Health check")
    print("  POST /api/train               - Train model")
    print("  POST /api/predict             - Predict signals")
    print("  POST /api/backtest            - Run backtest")
    print("  GET  /api/models              - List models")
    print("  GET  /api/model/<name>        - Get model info")
    print("  DEL  /api/model/<name>        - Delete model")
    print("  POST /api/model/<name>/eval   - Evaluate model")
    print("=" * 60)

    app.run(host='127.0.0.1', port=8766, debug=False)
