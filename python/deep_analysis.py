# -*- coding: utf-8 -*-
"""Deep analysis: regular stocks without names, market column values, l1 coverage."""
import duckdb
import os

DB = r'D:\Code\Quant Harness\AIQuant\build\bin\data\stock.duckdb'
con = duckdb.connect(DB, read_only=True)

print('=== stock_basic market column values (main DB) ===')
print(con.execute("SELECT market, COUNT(*) FROM stock_basic GROUP BY market ORDER BY 2 DESC").fetchall())

print('\n=== ohlc distinct symbols ===')
print('total distinct:', con.execute('SELECT COUNT(DISTINCT symbol) FROM ohlc').fetchone()[0])

print('\n=== regular-stock symbols in ohlc missing from l1_snapshot names ===')
# regular stock code ranges: 60x/688/900 (SH), 00x/30x/200 (SZ), 920 (BJ)
rows = con.execute("""
    SELECT o.symbol FROM (
        SELECT DISTINCT symbol FROM ohlc
    ) o
    WHERE (
        regexp_matches(o.symbol, '^sh(60[0135]|688|900)[0-9]{3}$')
        OR regexp_matches(o.symbol, '^sz(00[0123]|30[012]|200)[0-9]{3}$')
        OR regexp_matches(o.symbol, '^bj920[0-9]{3}$')
        OR regexp_matches(o.symbol, '^60[0135][0-9]{3}$')
        OR regexp_matches(o.symbol, '^00[0123][0-9]{3}$')
        OR regexp_matches(o.symbol, '^30[012][0-9]{3}$')
        OR regexp_matches(o.symbol, '^920[0-9]{3}$')
    )
    AND NOT EXISTS (
        SELECT 1 FROM l1_snapshot l
        WHERE l.symbol = o.symbol AND l.name IS NOT NULL AND l.name != ''
    )
""").fetchall()
print('regular stocks in ohlc WITHOUT l1 name:', len(rows))
for r in rows[:40]:
    print('  ', r[0])

print('\n=== l1_snapshot distinct symbols with valid name ===')
print(con.execute("SELECT COUNT(DISTINCT symbol) FROM l1_snapshot WHERE name IS NOT NULL AND name != ''").fetchone()[0])

print('\n=== ohlc symbol count with valid l1 name ===')
print(con.execute("""
    SELECT COUNT(*) FROM (
        SELECT DISTINCT o.symbol FROM ohlc o
        WHERE EXISTS (SELECT 1 FROM l1_snapshot l WHERE l.symbol = o.symbol AND l.name IS NOT NULL AND l.name != '')
    )
""").fetchone()[0])

print('\n=== sample: symbols in ohlc but NOT in stock_basic ===')
miss = con.execute("""
    SELECT DISTINCT o.symbol FROM ohlc o
    WHERE NOT EXISTS (SELECT 1 FROM stock_basic b WHERE b.symbol = o.symbol)
    ORDER BY o.symbol LIMIT 30
""").fetchall()
print('count:', con.execute("""
    SELECT COUNT(*) FROM (SELECT DISTINCT o.symbol FROM ohlc o
    WHERE NOT EXISTS (SELECT 1 FROM stock_basic b WHERE b.symbol = o.symbol))
""").fetchone()[0])
for r in miss:
    print('  ', r[0])

print('\n=== sample: stock_basic symbols NOT in ohlc ===')
only_basic = con.execute("""
    SELECT b.symbol FROM stock_basic b
    WHERE NOT EXISTS (SELECT 1 FROM ohlc o WHERE o.symbol = b.symbol)
    ORDER BY b.symbol LIMIT 30
""").fetchall()
print('count:', con.execute("""
    SELECT COUNT(*) FROM stock_basic b
    WHERE NOT EXISTS (SELECT 1 FROM ohlc o WHERE o.symbol = b.symbol)
""").fetchone()[0])
for r in only_basic:
    print('  ', r[0])

con.close()
