-- AIQuant Database Schema
-- V1 MVP Core Tables
-- Generated: 2026-08-17

-- Instruments 标的表
CREATE TABLE IF NOT EXISTS instruments (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    instrument_id VARCHAR(30) NOT NULL UNIQUE,
    symbol VARCHAR(20) NOT NULL,
    exchange VARCHAR(10) NOT NULL,
    country VARCHAR(5) NOT NULL,
    currency VARCHAR(3) NOT NULL DEFAULT 'USD',
    name VARCHAR(100) NOT NULL,
    asset_type VARCHAR(20) NOT NULL DEFAULT 'equity',
    sector VARCHAR(50),
    industry VARCHAR(50),
    listing_date DATE,
    delisting_date DATE,
    is_active INTEGER NOT NULL DEFAULT 1,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_instruments_symbol ON instruments(symbol);
CREATE INDEX IF NOT EXISTS idx_instruments_sector ON instruments(sector);
CREATE INDEX IF NOT EXISTS idx_instruments_active ON instruments(is_active);

-- Users 用户表
CREATE TABLE IF NOT EXISTS users (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    username VARCHAR(50) NOT NULL UNIQUE,
    email VARCHAR(100),
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

-- Settings 设置表
CREATE TABLE IF NOT EXISTS settings (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    market VARCHAR(10) NOT NULL DEFAULT 'US',
    trading_mode VARCHAR(20) NOT NULL DEFAULT 'paper',
    ai_provider VARCHAR(30) NOT NULL DEFAULT 'deepseek',
    ai_model VARCHAR(50) NOT NULL DEFAULT 'deepseek-chat',
    ai_api_key_ref VARCHAR(100),
    riskos_enabled INTEGER NOT NULL DEFAULT 0,
    riskos_api_key_ref VARCHAR(100),
    data_provider VARCHAR(30) NOT NULL DEFAULT 'yahoo',
    broker_type VARCHAR(30) NOT NULL DEFAULT 'paper',
    broker_api_key_ref VARCHAR(100),
    broker_api_secret_ref VARCHAR(100),
    initial_capital REAL NOT NULL DEFAULT 100000,
    currency VARCHAR(3) NOT NULL DEFAULT 'USD',
    auto_daily_run INTEGER NOT NULL DEFAULT 1,
    daily_run_time VARCHAR(5) NOT NULL DEFAULT '09:30',
    updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

-- Strategies 策略表
CREATE TABLE IF NOT EXISTS strategies (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    name VARCHAR(100) NOT NULL,
    description TEXT,
    strategy_type VARCHAR(30) NOT NULL,
    is_active INTEGER NOT NULL DEFAULT 1,
    config_json TEXT NOT NULL,
    universe_json TEXT NOT NULL,
    is_ai_generated INTEGER NOT NULL DEFAULT 0,
    ai_prompt TEXT,
    backtest_result_json TEXT,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_strategies_type ON strategies(strategy_type);
CREATE INDEX IF NOT EXISTS idx_strategies_active ON strategies(is_active);

-- Agents Agent 配置表
CREATE TABLE IF NOT EXISTS agents (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    agent_id VARCHAR(50) NOT NULL UNIQUE,
    name VARCHAR(100) NOT NULL,
    description TEXT,
    agent_type VARCHAR(30) NOT NULL,
    decision_weight REAL NOT NULL DEFAULT 0.2,
    system_prompt TEXT NOT NULL,
    tool_list_json TEXT NOT NULL,
    is_active INTEGER NOT NULL DEFAULT 1,
    health_score REAL DEFAULT 70.0,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

-- Agent Sessions 会话表
CREATE TABLE IF NOT EXISTS agent_sessions (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    session_id VARCHAR(50) NOT NULL UNIQUE,
    session_date DATE NOT NULL,
    status VARCHAR(20) NOT NULL,
    start_time TIMESTAMP,
    end_time TIMESTAMP,
    duration_ms INTEGER,
    confidence REAL,
    summary TEXT,
    token_usage INTEGER,
    decision_trace_id VARCHAR(50),
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_sessions_date ON agent_sessions(session_date);
CREATE INDEX IF NOT EXISTS idx_sessions_status ON agent_sessions(status);

-- Agent Tool Calls 工具调用表
CREATE TABLE IF NOT EXISTS agent_tool_calls (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    session_id VARCHAR(50) NOT NULL,
    agent_id VARCHAR(50) NOT NULL,
    tool_name VARCHAR(50) NOT NULL,
    tool_category VARCHAR(30) NOT NULL,
    input_json TEXT,
    output_json TEXT,
    call_status VARCHAR(20) NOT NULL,
    error_msg TEXT,
    call_duration_ms INTEGER,
    token_usage INTEGER,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_tool_calls_session ON agent_tool_calls(session_id);
CREATE INDEX IF NOT EXISTS idx_tool_calls_agent ON agent_tool_calls(agent_id);

-- Agent Decisions 决策表
CREATE TABLE IF NOT EXISTS agent_decisions (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    session_id VARCHAR(50) NOT NULL,
    agent_id VARCHAR(50) NOT NULL,
    decision_trace_id VARCHAR(50),
    decision_type VARCHAR(30) NOT NULL,
    result_json TEXT NOT NULL,
    confidence REAL,
    interpretation TEXT,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_decisions_session ON agent_decisions(session_id);
CREATE INDEX IF NOT EXISTS idx_decisions_type ON agent_decisions(decision_type);

-- Portfolios 组合表
CREATE TABLE IF NOT EXISTS portfolios (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    name VARCHAR(100) NOT NULL,
    portfolio_type VARCHAR(30) NOT NULL,
    strategy_id INTEGER,
    initial_capital REAL NOT NULL,
    current_capital REAL NOT NULL,
    cash REAL NOT NULL,
    total_pnl REAL NOT NULL DEFAULT 0,
    total_return REAL NOT NULL DEFAULT 0,
    sharpe_ratio REAL,
    max_drawdown REAL,
    is_active INTEGER NOT NULL DEFAULT 1,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

-- Positions 持仓表
CREATE TABLE IF NOT EXISTS positions (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    portfolio_id INTEGER NOT NULL,
    instrument_id VARCHAR(30) NOT NULL,
    quantity INTEGER NOT NULL,
    avg_cost REAL NOT NULL,
    current_price REAL,
    market_value REAL,
    unrealized_pnl REAL,
    unrealized_return REAL,
    weight REAL,
    position_status VARCHAR(20) NOT NULL DEFAULT 'open',
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_positions_portfolio ON positions(portfolio_id);
CREATE INDEX IF NOT EXISTS idx_positions_instrument ON positions(instrument_id);
CREATE INDEX IF NOT EXISTS idx_positions_status ON positions(position_status);

-- Orders 订单表
CREATE TABLE IF NOT EXISTS orders (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    order_id VARCHAR(50) NOT NULL UNIQUE,
    portfolio_id INTEGER NOT NULL,
    instrument_id VARCHAR(30) NOT NULL,
    decision_trace_id VARCHAR(50),
    order_type VARCHAR(20) NOT NULL,
    side VARCHAR(10) NOT NULL,
    quantity INTEGER NOT NULL,
    price REAL,
    limit_price REAL,
    stop_price REAL,
    status VARCHAR(20) NOT NULL,
    filled_quantity INTEGER,
    filled_price REAL,
    risk_check_id INTEGER,
    risk_approved INTEGER NOT NULL DEFAULT 0,
    submitted_at TIMESTAMP,
    filled_at TIMESTAMP,
    cancelled_at TIMESTAMP,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_orders_portfolio ON orders(portfolio_id);
CREATE INDEX IF NOT EXISTS idx_orders_status ON orders(status);
CREATE INDEX IF NOT EXISTS idx_orders_instrument ON orders(instrument_id);

-- Trades 成交表
CREATE TABLE IF NOT EXISTS trades (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    trade_id VARCHAR(50) NOT NULL UNIQUE,
    order_id VARCHAR(50) NOT NULL,
    portfolio_id INTEGER NOT NULL,
    instrument_id VARCHAR(30) NOT NULL,
    side VARCHAR(10) NOT NULL,
    quantity INTEGER NOT NULL,
    price REAL NOT NULL,
    gross_amount REAL NOT NULL,
    commission REAL NOT NULL DEFAULT 0,
    fees REAL NOT NULL DEFAULT 0,
    net_amount REAL NOT NULL,
    realized_pnl REAL,
    trade_date DATE NOT NULL,
    traded_at TIMESTAMP,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_trades_portfolio ON trades(portfolio_id);
CREATE INDEX IF NOT EXISTS idx_trades_date ON trades(trade_date);
CREATE INDEX IF NOT EXISTS idx_trades_instrument ON trades(instrument_id);

-- Risk Limits 风险限制表
CREATE TABLE IF NOT EXISTS risk_limits (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    portfolio_id INTEGER,
    max_position_pct REAL NOT NULL DEFAULT 0.3,
    max_total_exposure_pct REAL NOT NULL DEFAULT 1.0,
    max_leverage REAL NOT NULL DEFAULT 1.5,
    max_daily_loss_pct REAL NOT NULL DEFAULT 0.05,
    max_drawdown_pct REAL NOT NULL DEFAULT 0.2,
    max_order_size INTEGER NOT NULL DEFAULT 100000,
    max_daily_turnover_pct REAL NOT NULL DEFAULT 1.0,
    trading_hours_start VARCHAR(5) NOT NULL DEFAULT '09:30',
    trading_hours_end VARCHAR(5) NOT NULL DEFAULT '16:00',
    allow_trading_outside_hours INTEGER NOT NULL DEFAULT 0,
    is_active INTEGER NOT NULL DEFAULT 1,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

-- Risk Checks 风险检查表
CREATE TABLE IF NOT EXISTS risk_checks (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    check_id VARCHAR(50) NOT NULL UNIQUE,
    order_id VARCHAR(50),
    decision_trace_id VARCHAR(50),
    is_approved INTEGER NOT NULL DEFAULT 0,
    check_type VARCHAR(30) NOT NULL,
    checks_json TEXT NOT NULL,
    failed_checks_json TEXT,
    current_exposure_pct REAL,
    current_drawdown_pct REAL,
    current_daily_loss_pct REAL,
    checked_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_risk_checks_approved ON risk_checks(is_approved);
CREATE INDEX IF NOT EXISTS idx_risk_checks_type ON risk_checks(check_type);

-- Decision Traces 决策轨迹表
CREATE TABLE IF NOT EXISTS decision_traces (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    trace_id VARCHAR(50) NOT NULL UNIQUE,
    session_id VARCHAR(50) NOT NULL,
    decision_date DATE NOT NULL,
    market_regime VARCHAR(30),
    market_confidence REAL,
    alpha_summary TEXT,
    alpha_signals_json TEXT,
    risk_summary TEXT,
    risk_assessment_json TEXT,
    portfolio_summary TEXT,
    portfolio_plan_json TEXT,
    final_decision TEXT NOT NULL,
    target_exposure_pct REAL,
    trace_steps_json TEXT NOT NULL,
    risk_gate_passed INTEGER NOT NULL DEFAULT 0,
    risk_gate_notes TEXT,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_traces_date ON decision_traces(decision_date);
CREATE INDEX IF NOT EXISTS idx_traces_session ON decision_traces(session_id);

-- System Logs 系统日志表 (V1 MVP 可选)
CREATE TABLE IF NOT EXISTS system_logs (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    log_level VARCHAR(10) NOT NULL,
    source VARCHAR(50) NOT NULL,
    message TEXT NOT NULL,
    metadata_json TEXT,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_logs_level ON system_logs(log_level);
CREATE INDEX IF NOT EXISTS idx_logs_source ON system_logs(source);
CREATE INDEX IF NOT EXISTS idx_logs_time ON system_logs(created_at);
