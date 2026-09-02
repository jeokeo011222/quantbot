"""
QuantBot Easy-TDX 数据服务
基于 easy-tdx 开源库的通达信数据采集服务，提供 HTTP API 接口

功能：
1. 实时快照（五档、现价、涨跌、成交量）
2. K线数据（1min/5min/15min/30min/60min/day/week/month）
3. 逐笔成交
4. F10 资讯
5. 板块数据（行业/概念板块行情、成分股）
6. 资金流向（主力净流入、历史资金流）
7. 公告检索（巨潮资讯网）
8. 市场统计（涨跌家数、涨停跌停）
9. 技术指标（MACD/KDJ/RSI/BOLL等34种）

依赖：
    pip install easy-tdx flask flask-cors

使用：
    python easy_tdx_service.py
    默认监听 http://127.0.0.1:8765
"""

import json
import sys
import time
import traceback
from datetime import datetime
from typing import Optional

from flask import Flask, jsonify, request
from flask_cors import CORS

# ==================== 依赖检测 ====================
try:
    from easy_tdx import (
        Market,
        KlineCategory,
        UnifiedTdxClient,
        AsyncUnifiedTdxClient,
        ping_all,
        get_best_host,
    )
    from easy_tdx.mac.enums import Adjust, Period
    EASY_TDX_AVAILABLE = True
except ImportError:
    EASY_TDX_AVAILABLE = False
    print("[WARN] easy-tdx not installed. Run: pip install easy-tdx")
    print("[WARN] 降级模式：仅提供基础功能")

# ==================== 全局状态 ====================
app = Flask(__name__)
CORS(app)

_client: Optional[UnifiedTdxClient] = None
_async_client: Optional[AsyncUnifiedTdxClient] = None
_client_initialized = False
_stock_dict_cache = {}  # 本地股票名称缓存
_last_connect_time = 0


def _get_best_host_safe():
    """获取最佳 TDX 服务器地址"""
    try:
        if EASY_TDX_AVAILABLE:
            host = get_best_host()
            if host:
                return host
    except Exception:
        pass
    return "180.153.18.170"  # 默认主站


def init_client():
    """初始化 TDX 客户端"""
    global _client, _client_initialized
    if _client is not None and _client_initialized:
        return

    if not EASY_TDX_AVAILABLE:
        print("[TDX] easy-tdx not available, running in degraded mode")
        return

    try:
        host = _get_best_host_safe()
        print(f"[TDX] Connecting to {host}...")

        _client = UnifiedTdxClient(host)
        _client.connect()
        _client_initialized = True
        print(f"[TDX] Connected to {host}")
    except Exception as e:
        print(f"[TDX] Connection failed: {e}")
        _client_initialized = False


def ensure_client():
    """确保客户端已连接"""
    init_client()
    if _client is None or not _client_initialized:
        raise RuntimeError("TDX client not initialized. Check if easy-tdx is installed and network is available.")
    return _client


def _market_to_enum(market: str) -> Market:
    """将市场字符串转为 Market 枚举"""
    market = market.upper()
    if market in ("SH", "SSE", "1"):
        return Market.SH
    elif market in ("SZ", "SZSE", "0"):
        return Market.SZ
    elif market in ("BJ", "BSE", "2", "NEEQ"):
        try:
            return Market.BJ
        except AttributeError:
            return Market.SH
    raise ValueError(f"Unknown market: {market}")


def _period_to_category(period: str) -> KlineCategory:
    """将周期字符串转为 KlineCategory 枚举"""
    period_map = {
        "day": KlineCategory.DAY,
        "week": KlineCategory.WEEK,
        "month": KlineCategory.MONTH,
        "1min": KlineCategory.MIN1,
        "5min": KlineCategory.MIN5,
        "15min": KlineCategory.MIN15,
        "30min": KlineCategory.MIN30,
        "60min": KlineCategory.MIN60,
    }
    period_lower = period.lower()
    if period_lower in period_map:
        return period_map[period_lower]
    raise ValueError(f"Unknown period: {period}")


def _adjust_to_enum(adjust: str) -> Adjust:
    """将复权字符串转为 Adjust 枚举"""
    adjust_map = {
        "none": Adjust.NONE,
        "qfq": Adjust.QFQ,
        "hfq": Adjust.HFQ,
    }
    adjust_lower = adjust.lower()
    if adjust_lower in adjust_map:
        return adjust_map[adjust_lower]
    return Adjust.NONE


def _convert_bar(bar) -> dict:
    """将 easy_tdx K线数据转为标准格式"""
    if hasattr(bar, 'datetime'):
        dt = bar.datetime
    elif hasattr(bar, 'date'):
        dt = bar.date
    else:
        dt = str(getattr(bar, 'trade_date', ''))

    return {
        "datetime": str(dt),
        "open": float(getattr(bar, 'open', 0)),
        "high": float(getattr(bar, 'high', 0)),
        "low": float(getattr(bar, 'low', 0)),
        "close": float(getattr(bar, 'close', 0)),
        "volume": int(getattr(bar, 'vol', getattr(bar, 'volume', 0))),
        "amount": float(getattr(bar, 'amount', 0)),
    }


# ==================== 健康检查 ====================

@app.route("/health", methods=["GET"])
def health():
    """健康检查"""
    status = {
        "status": "ok",
        "easy_tdx_available": EASY_TDX_AVAILABLE,
        "client_connected": _client_initialized,
        "timestamp": time.time(),
    }
    if _client_initialized:
        status["host"] = _get_best_host_safe()
    return jsonify(status)


# ==================== 实时行情 ====================

@app.route("/api/quote/<market_code>", methods=["GET"])
def get_quote(market_code: str):
    """获取单只股票实时快照"""
    try:
        client = ensure_client()
        market = market_code[:2] if len(market_code) > 8 else market_code[:2]
        code = market_code[2:] if len(market_code) > 8 else market_code[2:]

        mkt = _market_to_enum(market)
        quote = client.get_security_quotes(mkt, [code])

        if quote and len(quote) > 0:
            q = quote[0]
            result = {
                "code": f"{market}{code}",
                "price": float(getattr(q, 'price', 0)),
                "last_close": float(getattr(q, 'last_close', 0)),
                "open": float(getattr(q, 'open', 0)),
                "high": float(getattr(q, 'high', 0)),
                "low": float(getattr(q, 'low', 0)),
                "volume": int(getattr(q, 'volume', 0)),
                "amount": float(getattr(q, 'amount', 0)),
                "bid_vols": [int(v) for v in getattr(q, 'bid_vols', [])],
                "ask_vols": [int(v) for v in getattr(q, 'ask_vols', [])],
                "bid_prices": [float(v) for v in getattr(q, 'bid_prices', [])],
                "ask_prices": [float(v) for v in getattr(q, 'ask_prices', [])],
            }
            return jsonify({"status": "ok", "data": result})
        else:
            return jsonify({"status": "error", "message": "No data returned"}), 404
    except Exception as e:
        return jsonify({"status": "error", "message": str(e)}), 500


@app.route("/api/quotes", methods=["GET"])
def get_quotes():
    """批量获取实时快照"""
    codes_str = request.args.get("codes", "")
    if not codes_str:
        return jsonify({"status": "error", "message": "codes parameter required"}), 400

    raw_codes = [c.strip() for c in codes_str.split(",") if c.strip()]
    try:
        client = ensure_client()
        results = []

        for mc in raw_codes:
            try:
                market = mc[:2].upper()
                code = mc[2:]
                mkt = _market_to_enum(market)
                quote = client.get_security_quotes(mkt, [code])
                if quote and len(quote) > 0:
                    q = quote[0]
                    results.append({
                        "code": mc,
                        "price": float(getattr(q, 'price', 0)),
                        "last_close": float(getattr(q, 'last_close', 0)),
                        "open": float(getattr(q, 'open', 0)),
                        "high": float(getattr(q, 'high', 0)),
                        "low": float(getattr(q, 'low', 0)),
                        "volume": int(getattr(q, 'volume', 0)),
                        "amount": float(getattr(q, 'amount', 0)),
                    })
                else:
                    results.append({"code": mc, "price": 0, "error": "No data"})
            except Exception as e:
                results.append({"code": mc, "error": str(e)})

        return jsonify({"status": "ok", "data": results})
    except Exception as e:
        return jsonify({"status": "error", "message": str(e)}), 500


# ==================== K线数据 ====================

@app.route("/api/kline", methods=["GET"])
def get_kline():
    """获取K线数据"""
    market = request.args.get("market", "")
    code = request.args.get("code", "")
    period = request.args.get("period", "day")
    count = int(request.args.get("count", "120"))
    adjust = request.args.get("adjust", "none")

    if not market or not code:
        return jsonify({"status": "error", "message": "market and code required"}), 400

    try:
        client = ensure_client()
        mkt = _market_to_enum(market)
        cat = _period_to_category(period)
        adj = _adjust_to_enum(adjust)

        bars = client.get_security_bars(mkt, code, cat, 0, count)
        bars_data = [_convert_bar(b) for b in bars]

        if adjust != "none" and hasattr(client, 'adjust_bars'):
            try:
                bars_data = client.adjust_bars(bars_data, adj)
            except Exception:
                pass

        return jsonify({
            "status": "ok",
            "data": {
                "market": market,
                "code": code,
                "period": period,
                "count": len(bars_data),
                "bars": bars_data,
            },
        })
    except Exception as e:
        return jsonify({"status": "error", "message": str(e)}), 500


@app.route("/api/minute_bars", methods=["GET"])
def get_minute_bars():
    """获取分时数据"""
    market = request.args.get("market", "")
    code = request.args.get("code", "")
    count = int(request.args.get("count", "240"))

    if not market or not code:
        return jsonify({"status": "error", "message": "market and code required"}), 400

    try:
        client = ensure_client()
        mkt = _market_to_enum(market)
        bars = client.get_minute_bars(mkt, code, 0, count)

        result = []
        for b in bars:
            result.append({
                "time": str(getattr(b, 'datetime', getattr(b, 'time', ''))),
                "price": float(getattr(b, 'price', getattr(b, 'close', 0))),
                "volume": int(getattr(b, 'volume', 0)),
                "avg_price": float(getattr(b, 'avg_price', 0)),
            })

        return jsonify({"status": "ok", "data": result})
    except Exception as e:
        return jsonify({"status": "error", "message": str(e)}), 500


# ==================== 逐笔成交 ====================

@app.route("/api/trades/<market_code>", methods=["GET"])
def get_trades(market_code: str):
    """获取逐笔成交"""
    try:
        client = ensure_client()
        market = market_code[:2].upper()
        code = market_code[2:]
        mkt = _market_to_enum(market)

        trades = client.get_transaction_data(mkt, code, 0, 5000)
        trade_list = []
        for t in trades:
            trade_list.append({
                "price": float(getattr(t, 'price', 0)),
                "volume": int(getattr(t, 'vol', getattr(t, 'volume', 0))),
                "time": str(getattr(t, 'time', '')),
                "bs": str(getattr(t, 'bs', '')),
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


# ==================== F10 资讯 ====================

@app.route("/api/f10/<market_code>", methods=["GET"])
def get_f10(market_code: str):
    """获取F10资讯"""
    try:
        client = ensure_client()
        market = market_code[:2].upper()
        code = market_code[2:]
        mkt = _market_to_enum(market)

        info = client.get_security_info(mkt, code)
        result = {
            "market_code": market_code,
            "info": {},
        }
        if info:
            result["info"] = {
                "name": str(getattr(info, 'name', '')),
                "code": str(getattr(info, 'code', '')),
                "company": str(getattr(info, 'company', '')),
            }

        return jsonify({"status": "ok", "data": result})
    except Exception as e:
        return jsonify({"status": "error", "message": str(e)}), 500


# ==================== 板块数据 ====================

@app.route("/api/board_list", methods=["GET"])
def get_board_list():
    """获取板块列表"""
    board_type = request.args.get("type", "HY")  # HY=行业, GN=概念

    try:
        client = ensure_client()
        if hasattr(client, 'get_board_list'):
            boards = client.get_board_list(board_type)
            result = []
            for b in boards:
                result.append({
                    "code": str(getattr(b, 'code', '')),
                    "name": str(getattr(b, 'name', '')),
                    "count": int(getattr(b, 'count', 0)),
                })
            return jsonify({"status": "ok", "data": result})
        else:
            return jsonify({"status": "error", "message": "Board list not supported"}), 501
    except Exception as e:
        return jsonify({"status": "error", "message": str(e)}), 500


@app.route("/api/board_summary", methods=["GET"])
def get_board_summary():
    """获取板块行情汇总"""
    board_code = request.args.get("code", "")
    members = request.args.get("members", "false").lower() == "true"

    if not board_code:
        return jsonify({"status": "error", "message": "code required"}), 400

    try:
        client = ensure_client()
        if hasattr(client, 'get_board_summary'):
            summary = client.get_board_summary(board_code)
            result = {
                "code": board_code,
                "name": str(getattr(summary, 'name', '')),
                "amount": float(getattr(summary, 'amount', 0)),
                "net_flow": float(getattr(summary, 'net_flow', 0)),
            }

            if members and hasattr(client, 'get_board_members'):
                member_list = client.get_board_members(board_code)
                result["members"] = [{
                    "code": str(getattr(m, 'code', '')),
                    "name": str(getattr(m, 'name', '')),
                    "price": float(getattr(m, 'price', 0)),
                    "change_pct": float(getattr(m, 'change_pct', 0)),
                } for m in member_list[:50]]

            return jsonify({"status": "ok", "data": result})
        else:
            return jsonify({"status": "error", "message": "Board summary not supported"}), 501
    except Exception as e:
        return jsonify({"status": "error", "message": str(e)}), 500


# ==================== 资金流向 ====================

@app.route("/api/fund_flow", methods=["GET"])
def get_fund_flow():
    """获取资金流向"""
    market = request.args.get("market", "")
    code = request.args.get("code", "")

    if not market or not code:
        return jsonify({"status": "error", "message": "market and code required"}), 400

    try:
        client = ensure_client()
        mkt = _market_to_enum(market)
        if hasattr(client, 'get_history_fund_flow'):
            flows = client.get_history_fund_flow(mkt, code)
            result = [{
                "date": str(getattr(f, 'date', '')),
                "net_amount": float(getattr(f, 'net_amount', 0)),
                "net_volume": float(getattr(f, 'net_volume', 0)),
            } for f in flows]
            return jsonify({"status": "ok", "data": result})
        else:
            return jsonify({"status": "error", "message": "Fund flow not supported"}), 501
    except Exception as e:
        return jsonify({"status": "error", "message": str(e)}), 500


@app.route("/api/capital_flow/<market_code>", methods=["GET"])
def get_capital_flow(market_code: str):
    """获取实时资金流向"""
    try:
        client = ensure_client()
        market = market_code[:2].upper()
        code = market_code[2:]
        mkt = _market_to_enum(market)

        if hasattr(client, 'get_fund_flow'):
            flow = client.get_fund_flow(mkt, code)
            result = {}
            if flow:
                result = {
                    "main_net": float(getattr(flow, 'main_net', 0)),
                    "super_large_net": float(getattr(flow, 'super_large_net', 0)),
                    "large_net": float(getattr(flow, 'large_net', 0)),
                    "medium_net": float(getattr(flow, 'medium_net', 0)),
                    "small_net": float(getattr(flow, 'small_net', 0)),
                }
            return jsonify({"status": "ok", "data": result})
        else:
            return jsonify({"status": "error", "message": "Capital flow not supported"}), 501
    except Exception as e:
        return jsonify({"status": "error", "message": str(e)}), 500


# ==================== 公告检索 ====================

@app.route("/api/announcement", methods=["GET"])
def get_announcement():
    """获取公告（巨潮资讯网）"""
    code = request.args.get("code", "")
    count = int(request.args.get("count", "30"))

    if not code:
        return jsonify({"status": "error", "message": "code required"}), 400

    try:
        import requests as req
        # 巨潮资讯网接口
        url = f"http://www.cninfo.com.cn/new/hisAnnouncement/query"
        headers = {
            "User-Agent": "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36",
            "Accept": "application/json",
        }
        data = {
            "stock": f"{code}",
            "tabName": "fulltext",
            "pageSize": count,
            "pageNum": 1,
            "column": "szse",
            "category": "",
            "plate": "",
            "seDate": "",
            "searchkey": "",
            "secid": "",
            "sortName": "",
            "sortType": "",
            "isHLtitle": "true",
        }
        resp = req.post(url, headers=headers, data=data, timeout=10)
        if resp.status_code == 200:
            result = resp.json()
            announcements = result.get("announcements", [])
            formatted = [{
                "title": a.get("announcementTitle", ""),
                "date": a.get("announcementTime", ""),
                "url": f"http://static.cninfo.com.cn/{a.get('adjunctUrl', '')}",
                "type": a.get("announcementType", ""),
            } for a in announcements[:count]]
            return jsonify({"status": "ok", "data": formatted})
        else:
            return jsonify({"status": "error", "message": f"HTTP {resp.status_code}"}), 500
    except ImportError:
        return jsonify({"status": "error", "message": "requests library not installed"}), 500
    except Exception as e:
        return jsonify({"status": "error", "message": str(e)}), 500


# ==================== 市场统计 ====================

@app.route("/api/market_stat", methods=["GET"])
def get_market_stat():
    """获取市场统计"""
    try:
        client = ensure_client()
        if hasattr(client, 'get_market_stat'):
            stat = client.get_market_stat()
            result = {}
            if stat:
                result = {
                    "up_count": int(getattr(stat, 'up_count', 0)),
                    "down_count": int(getattr(stat, 'down_count', 0)),
                    "flat_count": int(getattr(stat, 'flat_count', 0)),
                    "limit_up": int(getattr(stat, 'limit_up', 0)),
                    "limit_down": int(getattr(stat, 'limit_down', 0)),
                }
            return jsonify({"status": "ok", "data": result})
        else:
            # 降级：自行统计
            mkt = Market.SH
            count = client.get_security_count(mkt)
            return jsonify({
                "status": "ok",
                "data": {
                    "total_stocks_sh": count,
                    "message": "Basic count only (detailed stat requires full TDX data)",
                },
            })
    except Exception as e:
        return jsonify({"status": "error", "message": str(e)}), 500


# ==================== 股票搜索 ====================

@app.route("/api/search", methods=["GET"])
def search_stocks():
    """搜索股票"""
    keyword = request.args.get("keyword", "")
    market = request.args.get("market", "all")

    if not keyword:
        return jsonify({"status": "error", "message": "keyword required"}), 400

    try:
        client = ensure_client()
        results = []

        for mkt_enum, mkt_name in [(Market.SH, "sh"), (Market.SZ, "sz")]:
            try:
                count = client.get_security_count(mkt_enum)
                list_data = client.get_security_list(mkt_enum, 0, min(count, 5000))
                for info in list_data:
                    code = str(getattr(info, 'code', ''))
                    name = str(getattr(info, 'name', ''))
                    if (keyword.lower() in code.lower() or
                            keyword.lower() in name.lower()):
                        if market == "all" or market == mkt_name:
                            results.append({
                                "code": f"{mkt_name}{code}",
                                "name": name,
                                "market": mkt_name,
                            })
                if len(results) >= 20:
                    break
            except Exception:
                continue

        return jsonify({"status": "ok", "data": results[:20]})
    except Exception as e:
        return jsonify({"status": "error", "message": str(e)}), 500


# ==================== 技术指标 ====================

@app.route("/api/indicator", methods=["GET"])
def get_indicator():
    """计算技术指标"""
    market = request.args.get("market", "")
    code = request.args.get("code", "")
    indicator_name = request.args.get("name", "MACD")
    period = request.args.get("period", "day")
    count = int(request.args.get("count", 120))

    if not market or not code:
        return jsonify({"status": "error", "message": "market and code required"}), 400

    if not EASY_TDX_AVAILABLE:
        return jsonify({"status": "error", "message": "easy-tdx not installed"}), 500

    try:
        from easy_tdx.indicators import calc_indicator

        client = ensure_client()
        mkt = _market_to_enum(market)
        cat = _period_to_category(period)

        bars = client.get_security_bars(mkt, code, cat, 0, count)
        rows = []
        for b in bars:
            rows.append({
                'open': float(getattr(b, 'open', 0)),
                'close': float(getattr(b, 'close', 0)),
                'high': float(getattr(b, 'high', 0)),
                'low': float(getattr(b, 'low', 0)),
                'volume': float(getattr(b, 'vol', 0)),
            })

        result = calc_indicator(indicator_name, rows)
        return jsonify({"status": "ok", "data": result})
    except ImportError:
        return jsonify({"status": "error", "message": "indicators module not available"}), 501
    except Exception as e:
        return jsonify({"status": "error", "message": str(e)}), 500


# ==================== 服务器信息 ====================

@app.route("/api/server_info", methods=["GET"])
def get_server_info():
    """获取TDX服务器信息"""
    try:
        if EASY_TDX_AVAILABLE:
            from easy_tdx.transport.sync import ping_all, KNOWN_HOSTS, MAC_HOSTS
            hosts_info = []
            for host in (KNOWN_HOSTS or [])[:5]:
                try:
                    latency = ping_all([host])
                    hosts_info.append({"host": host, "latency": latency})
                except Exception:
                    hosts_info.append({"host": host, "latency": None})
            return jsonify({
                "status": "ok",
                "data": {
                    "known_hosts_count": len(KNOWN_HOSTS or []),
                    "hosts": hosts_info,
                    "client_connected": _client_initialized,
                },
            })
        else:
            return jsonify({"status": "ok", "data": {"message": "easy-tdx not installed"}})
    except Exception as e:
        return jsonify({"status": "error", "message": str(e)}), 500


# ==================== 启动 ====================

def create_app(host="127.0.0.1", port=8765):
    """创建并启动服务"""
    print(f"{'='*60}")
    print(f"  QuantBot Easy-TDX 数据服务")
    print(f"{'='*60}")
    print(f"  easy-tdx 可用: {EASY_TDX_AVAILABLE}")
    print(f"  监听地址: http://{host}:{port}")

    if EASY_TDX_AVAILABLE:
        print(f"  正在连接 TDX 行情服务器...")
        try:
            init_client()
            if _client_initialized:
                print(f"  TDX 连接成功 ✓")
            else:
                print(f"  TDX 连接失败，将在首次请求时重试")
        except Exception as e:
            print(f"  TDX 连接异常: {e}")

    print(f"{'='*60}")
    app.run(host=host, port=port, debug=False)


if __name__ == "__main__":
    port = int(sys.argv[1]) if len(sys.argv) > 1 else 8765
    create_app(port=port)