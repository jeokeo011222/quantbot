"""
QuantBot TDX 数据服务
基于 eltdx 的通达信数据采集服务，提供 HTTP API 接口

功能：
1. 实时快照（五档、现价、涨跌、成交量）
2. K线数据（1min/5min/15min/30min/60min/day/week/month）
3. 逐笔成交
4. F10 资讯

依赖：
    pip install eltdx flask flask-cors

使用：
    python tdx_service.py
    默认监听 http://127.0.0.1:8765

API 端点：
    GET /health                     健康检查
    GET /api/quote/{market}{code}   实时快照
    GET /api/kline                  K线数据
    GET /api/trades/{market}{code}  逐笔成交
    GET /api/f10/{market}{code}     F10资讯
    GET /api/quotes                 批量实时快照
    GET /api/search                 搜索股票
"""

import asyncio
import json
import sys
import time
from typing import Optional

from flask import Flask, jsonify, request
from flask_cors import CORS

try:
    from eltdx import TdxClient
    ELTDX_AVAILABLE = True
except ImportError:
    ELTDX_AVAILABLE = False
    print("[WARN] eltdx not installed. Run: pip install eltdx")


app = Flask(__name__)
CORS(app)

# 全局客户端和事件循环
_client: Optional[TdxClient] = None
_event_loop: Optional[asyncio.AbstractEventLoop] = None
_client_connected = False


def get_event_loop() -> asyncio.AbstractEventLoop:
    """获取或创建事件循环"""
    global _event_loop
    if _event_loop is None or _event_loop.is_closed():
        _event_loop = asyncio.new_event_loop()
        asyncio.set_event_loop(_event_loop)
    return _event_loop


def run_async(coro):
    """在线程中运行异步任务"""
    loop = get_event_loop()
    future = asyncio.run_coroutine_threadsafe(coro, loop)
    return future.result(timeout=30)


async def connect_client():
    """连接通达信行情主站"""
    global _client, _client_connected
    if _client is not None and _client_connected:
        return _client

    _client = TdxClient()
    try:
        await _client.connect()
        _client_connected = True
        print("[TDX] Connected to TDX quote server")
    except Exception as e:
        print(f"[TDX] Connect failed: {e}")
        _client_connected = False
        raise
    return _client


def ensure_connected():
    """确保客户端已连接"""
    global _client
    if not ELTDX_AVAILABLE:
        raise RuntimeError("eltdx not installed")

    if _client is None or not _client_connected:
        run_async(connect_client)
    return _client


def format_bar(bar) -> dict:
    """格式化K线数据"""
    return {
        "datetime": str(getattr(bar, "datetime", "")),
        "open": float(getattr(bar, "open", 0)),
        "high": float(getattr(bar, "high", 0)),
        "low": float(getattr(bar, "low", 0)),
        "close": float(getattr(bar, "close", 0)),
        "volume": int(getattr(bar, "volume", 0)),
        "amount": float(getattr(bar, "amount", 0)),
    }


@app.route("/health", methods=["GET"])
def health():
    """健康检查"""
    return jsonify({
        "status": "ok",
        "eltdx_available": ELTDX_AVAILABLE,
        "client_connected": _client_connected,
        "timestamp": time.time(),
    })


@app.route("/api/quote/<market_code>", methods=["GET"])
def get_quote(market_code: str):
    """
    获取实时快照
    market_code: 如 sh600519, sz000001
    """
    try:
        client = ensure_connected()
        quote = run_async(client.get_quote(market_code))
        result = {}
        if isinstance(quote, dict):
            result = quote
        else:
            result = {
                "price": float(getattr(quote, "price", 0)),
                "last_close": float(getattr(quote, "last_close", 0)),
                "open": float(getattr(quote, "open", 0)),
                "high": float(getattr(quote, "high", 0)),
                "low": float(getattr(quote, "low", 0)),
                "volume": int(getattr(quote, "volume", 0)),
                "amount": float(getattr(quote, "amount", 0)),
                "bid_vols": list(getattr(quote, "bid_vols", [])),
                "ask_vols": list(getattr(quote, "ask_vols", [])),
            }
        return jsonify({"status": "ok", "data": result})
    except Exception as e:
        return jsonify({"status": "error", "message": str(e)}), 500


@app.route("/api/quotes", methods=["GET"])
def get_quotes():
    """
    批量获取实时快照
    codes: 逗号分隔的代码列表，如 sh600519,sz000001,sz300750
    """
    codes_str = request.args.get("codes", "")
    if not codes_str:
        return jsonify({"status": "error", "message": "codes parameter required"}), 400

    codes = [c.strip() for c in codes_str.split(",") if c.strip()]
    try:
        client = ensure_connected()
        quotes = run_async(client.get_quote_list(codes))
        results = []
        for code, quote in zip(codes, quotes if isinstance(quotes, list) else [quotes]):
            if isinstance(quote, dict):
                q = quote
            else:
                q = {
                    "price": float(getattr(quote, "price", 0)),
                    "last_close": float(getattr(quote, "last_close", 0)),
                    "open": float(getattr(quote, "open", 0)),
                    "high": float(getattr(quote, "high", 0)),
                    "low": float(getattr(quote, "low", 0)),
                    "volume": int(getattr(quote, "volume", 0)),
                    "amount": float(getattr(quote, "amount", 0)),
                }
            q["code"] = code
            results.append(q)
        return jsonify({"status": "ok", "data": results})
    except Exception as e:
        return jsonify({"status": "error", "message": str(e)}), 500


@app.route("/api/kline", methods=["GET"])
def get_kline():
    """
    获取K线数据
    参数：
        market: sh/sz/bj
        code: 股票代码
        period: 1min/5min/15min/30min/60min/day/week/month
        count: 获取数量，默认120
        adjust: none/qfq/hfq，默认none
    """
    market = request.args.get("market", "")
    code = request.args.get("code", "")
    period = request.args.get("period", "day")
    count = int(request.args.get("count", "120"))
    adjust = request.args.get("adjust", "none")

    if not market or not code:
        return jsonify({"status": "error", "message": "market and code required"}), 400

    try:
        client = ensure_connected()
        kline = run_async(
            client.get_kline(
                market=market,
                code=code,
                period=period,
                count=count,
                adjust=adjust,
            )
        )
        bars = [format_bar(b) for b in kline]
        return jsonify({
            "status": "ok",
            "data": {
                "market": market,
                "code": code,
                "period": period,
                "count": len(bars),
                "bars": bars,
            },
        })
    except Exception as e:
        return jsonify({"status": "error", "message": str(e)}), 500


@app.route("/api/trades/<market_code>", methods=["GET"])
def get_trades(market_code: str):
    """
    获取逐笔成交
    market_code: 如 sh600519, sz000001
    注意：非交易时段可能返回空
    """
    try:
        client = ensure_connected()
        trades = run_async(client.get_trades(market_code))
        trade_list = []
        if trades and isinstance(trades, list):
            for t in trades:
                if isinstance(t, dict):
                    trade_list.append(t)
                else:
                    trade_list.append({
                        "price": float(getattr(t, "price", 0)),
                        "volume": int(getattr(t, "volume", 0)),
                        "time": str(getattr(t, "time", "")),
                        "bs": str(getattr(t, "bs", "")),
                    })
        return jsonify({
            "status": "ok",
            "data": {
                "market_code": market_code,
                "count": len(trade_list),
                "trades": trade_list,
            },
        })
    except Exception as e:
        return jsonify({"status": "error", "message": str(e)}), 500


@app.route("/api/f10/<market_code>", methods=["GET"])
def get_f10(market_code: str):
    """
    获取F10资讯（HTML原始文本）
    market_code: 如 sh600519, sz000001
    """
    try:
        client = ensure_connected()
        f10_html = run_async(client.get_f10(market_code))
        return jsonify({
            "status": "ok",
            "data": {
                "market_code": market_code,
                "length": len(f10_html) if f10_html else 0,
                "html": f10_html if f10_html else "",
            },
        })
    except Exception as e:
        return jsonify({"status": "error", "message": str(e)}), 500


@app.route("/api/search", methods=["GET"])
def search_stocks():
    """
    搜索股票
    参数：
        keyword: 搜索关键词（代码或名称）
        market: sh/sz/all，默认all
    注意：此接口使用本地缓存数据，需要先运行 index_stocks.py 建立索引
    """
    keyword = request.args.get("keyword", "")
    market = request.args.get("market", "all")

    # 尝试加载本地索引
    index_path = "stock_index.json"
    try:
        with open(index_path, "r", encoding="utf-8") as f:
            index = json.load(f)
    except (FileNotFoundError, json.JSONDecodeError):
        return jsonify({
            "status": "warning",
            "message": "Stock index not found. Run index_stocks.py first.",
            "data": [],
        })

    results = []
    keyword_lower = keyword.lower()
    for stock in index:
        if market != "all" and stock.get("market") != market:
            continue
        if keyword_lower in stock.get("code", "").lower() or keyword_lower in stock.get("name", "").lower():
            results.append(stock)
        if len(results) >= 20:
            break

    return jsonify({"status": "ok", "data": results})


def create_app(host="127.0.0.1", port=8765):
    """创建并启动服务"""
    print(f"[QuantBot TDX Service] Starting on {host}:{port}")
    print(f"[QuantBot TDX Service] eltdx available: {ELTDX_AVAILABLE}")

    if ELTDX_AVAILABLE:
        print("[QuantBot TDX Service] Connecting to TDX...")
        try:
            loop = get_event_loop()
            asyncio.run_coroutine_threadsafe(connect_client(), loop)
            print("[QuantBot TDX Service] TDX connected successfully")
        except Exception as e:
            print(f"[QuantBot TDX Service] TDX connection failed: {e}")
            print("[QuantBot TDX Service] Service will start in degraded mode")

    app.run(host=host, port=port, debug=False)


if __name__ == "__main__":
    port = int(sys.argv[1]) if len(sys.argv) > 1 else 8765
    create_app(port=port)
