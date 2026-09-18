-- QuantBot DuckDB Views
-- Parquet views for historical market data
-- V1 MVP

-- US Market Daily View
CREATE OR REPLACE VIEW us_market_daily AS
SELECT
    instrument_id,
    trade_date,
    open,
    high,
    low,
    close,
    volume,
    turnover,
    raw_close,
    adjustment_factor
FROM read_parquet(
    '~/.quantpilot/market/us/daily/year=*/part-*.parquet',
    HIVE_PARTITIONING=1
);

-- CN Market Daily View
CREATE OR REPLACE VIEW cn_market_daily AS
SELECT
    instrument_id,
    trade_date,
    open,
    high,
    low,
    close,
    volume,
    turnover,
    raw_close,
    adjustment_factor
FROM read_parquet(
    '~/.quantpilot/market/cn/daily/year=*/part-*.parquet',
    HIVE_PARTITIONING=1
);

-- HK Market Daily View
CREATE OR REPLACE VIEW hk_market_daily AS
SELECT
    instrument_id,
    trade_date,
    open,
    high,
    low,
    close,
    volume,
    turnover,
    raw_close,
    adjustment_factor
FROM read_parquet(
    '~/.quantpilot/market/hk/daily/year=*/part-*.parquet',
    HIVE_PARTITIONING=1
);

-- Market Stats View (US)
CREATE OR REPLACE VIEW us_market_stats AS
SELECT
    sector,
    COUNT(*) as stock_count,
    AVG(close) as avg_close,
    SUM(volume) as total_volume,
    MAX(high) as max_high,
    MIN(low) as min_low
FROM us_market_daily
WHERE trade_date = (SELECT MAX(trade_date) FROM us_market_daily)
GROUP BY sector;

-- Factor Calculation View
CREATE OR REPLACE VIEW price_factors AS
SELECT
    instrument_id,
    trade_date,
    close,
    close / LAG(close, 1) OVER (PARTITION BY instrument_id ORDER BY trade_date) - 1 AS daily_return,
    close / LAG(close, 5) OVER (PARTITION BY instrument_id ORDER BY trade_date) - 1 AS weekly_return,
    close / LAG(close, 21) OVER (PARTITION BY instrument_id ORDER BY trade_date) - 1 AS monthly_return,
    close / LAG(close, 63) OVER (PARTITION BY instrument_id ORDER BY trade_date) - 1 AS quarterly_return,
    AVG(close) OVER (PARTITION BY instrument_id ORDER BY trade_date ROWS BETWEEN 19 PRECEDING AND CURRENT ROW) AS sma_20,
    AVG(close) OVER (PARTITION BY instrument_id ORDER BY trade_date ROWS BETWEEN 49 PRECEDING AND CURRENT ROW) AS sma_50,
    STDDEV_POP(close) OVER (PARTITION BY instrument_id ORDER BY trade_date ROWS BETWEEN 19 PRECEDING AND CURRENT ROW) AS volatility_20
FROM us_market_daily;
