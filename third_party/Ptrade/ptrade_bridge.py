# -*- coding: utf-8 -*-
"""
QuantBot Ptrade 桥接策略
运行于恒生 Ptrade 客户端内嵌 Python 引擎中，提供本地 HTTP 网关，
供 QuantBot 宿主（Go）下发订单 / 查询持仓资金 / 拉取成交回报。

【免责声明】此程序仅供学习测试使用，严禁用于实盘。

用法：
  1. 打开 Ptrade 客户端 → 策略交易 → 新建 Python 策略
  2. 粘贴本脚本并保存
  3. 在【交易接口】页面对应目录下确认 config.json 端口（默认 8891）
  4. 启动策略；QuantBot 中执行「测试连接」即完成对接
"""
import json
import os
import threading
import time

try:
    from http.server import BaseHTTPRequestHandler, HTTPServer
except Exception:
    HTTPServer = None

# ==================== 配置加载 ====================
_CONFIG_PATH = os.path.join(os.path.dirname(os.path.abspath(__file__)), "config.json")

_config = {"host": "127.0.0.1", "port": 8891, "account": "", "poll_interval": 1}


def _load_config():
    global _config
    try:
        with open(_CONFIG_PATH, "r", encoding="utf-8") as f:
            cfg = json.load(f)
        _config.update(cfg)
    except Exception:
        pass
    return _config


# ==================== 成交回报环形记录 ====================
_fills = []          # [{seq,order_id,symbol,side,volume,price,traded_at}]
_fill_seq = 0
_seen_symbols = set()  # 已处理的委托号（去重）


def _record_fill(order_id, symbol, side, volume, price):
    global _fill_seq
    key = (order_id, symbol, side, volume, price)
    if key in _seen_symbols:
        return
    _seen_symbols.add(key)
    _fill_seq += 1
    _fills.append({"seq": _fill_seq, "order_id": str(order_id), "symbol": symbol,
                   "side": side, "volume": volume, "price": price,
                   "traded_at": time.strftime("%Y-%m-%d %H:%M:%S")})
    if len(_fills) > 2000:
        _fills[: len(_fills) - 2000] = []
    return _fill_seq


def _collect_fills():
    """handle_data 每轮调用：尝试从 Ptrade 成交列表收集新成交（防御式，API 缺失时静默跳过）。"""
    try:
        trades = get_trades()
        for t in trades:
            oid = getattr(t, "order_id", getattr(t, "entrust_no", ""))
            if not oid:
                continue
            side = "BUY" if getattr(t, "order_bs", getattr(t, "entrust_bs", "B")).startswith(("B", "买")) else "SELL"
            vol = getattr(t, "volume", getattr(t, "business_amount", 0))
            price = getattr(t, "price", getattr(t, "business_price", 0))
            sym = getattr(t, "security", "")
            if sym:
                _record_fill(oid, sym, side, vol, price)
    except Exception:
        pass


# ==================== 桥接网关 ====================
class _QuantBotHandler(BaseHTTPRequestHandler):
    def _send(self, obj, code=200):
        try:
            body = json.dumps(obj, ensure_ascii=False).encode("utf-8")
            self.send_response(code)
            self.send_header("Content-Type", "application/json; charset=utf-8")
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            self.wfile.write(body)
        except Exception:
            pass

    def log_message(self, fmt, *args):
        pass

    def do_GET(self):
        path = self.path.split("?")[0]
        if path == "/status":
            return self._send({"ok": True, "connected": True, "account": _config.get("account", ""),
                               "message": "Ptrade 桥接策略运行中"})
        if path == "/positions":
            return self._handle_positions()
        if path == "/asset":
            return self._handle_asset()
        if path == "/fills":
            return self._handle_fills(self.path)
        return self._send({"ok": False, "message": "未知路径: %s" % path}, 404)

    def do_POST(self):
        try:
            length = int(self.headers.get("Content-Length", 0))
            req = json.loads(self.rfile.read(length) or b"{}")
        except Exception:
            return self._send({"ok": False, "message": "请求体解析失败"})
        path = self.path.split("?")[0]
        if path == "/order":
            return self._handle_order(req)
        if path == "/cancel":
            return self._handle_cancel(req)
        return self._send({"ok": False, "message": "未知路径: %s" % path}, 404)

    # ---- 下单 ----
    def _handle_order(self, req):
        action = req.get("action", "")
        symbol = req.get("symbol", "")
        volume = int(req.get("volume") or 0)
        price = float(req.get("price") or 0)
        if action not in ("buy", "sell") or not symbol or volume <= 0:
            return self._send({"ok": False, "message": "参数缺失：需 action(buy/sell)+symbol+volume"})
        try:
            if price > 0:
                style = {"PriceLimit": price}  # 限价
            else:
                style = {"OrderType": 1}        # 对手价（市价）
            oid = order(symbol, volume, style=style) if action == "buy" else order(symbol, -volume, style=style)
            return self._send({"ok": True, "order_id": str(oid)})
        except Exception as e:
            return self._send({"ok": False, "message": "下单失败: %s" % e})

    # ---- 撤单 ----
    def _handle_cancel(self, req):
        oid = req.get("order_id", "")
        if not oid:
            return self._send({"ok": False, "message": "缺少 order_id"})
        try:
            cancel_order(str(oid))
            return self._send({"ok": True})
        except Exception as e:
            return self._send({"ok": False, "message": "撤单失败: %s" % e})

    # ---- 持仓 ----
    def _handle_positions(self):
        try:
            positions = []
            for p in get_positions():
                if getattr(p, "security", "") == "":
                    continue
                positions.append({
                    "symbol": getattr(p, "security", ""),
                    "quantity": getattr(p, "amount", getattr(p, "current_amount", 0)),
                    "available": getattr(p, "available_amount", 0),
                    "cost_price": getattr(p, "avg_cost", 0),
                })
            return self._send({"ok": True, "positions": positions})
        except Exception as e:
            return self._send({"ok": False, "message": "查询持仓失败: %s" % e})

    # ---- 资金 ----
    def _handle_asset(self):
        try:
            b = query_balance()
            # query_balance() 可能返回 dict 或对象，统一提取
            if isinstance(b, dict):
                cash = b.get("cash", 0)
                market_value = b.get("market_value", 0)
                total = b.get("total_assets", b.get("totalasset", cash + market_value))
                frozen = b.get("frozen", b.get("freeze_cash", 0))
            else:
                cash = getattr(b, "cash", 0)
                market_value = getattr(b, "market_value", 0)
                total = getattr(b, "total_assets", getattr(b, "totalasset", cash + market_value))
                frozen = getattr(b, "frozen", getattr(b, "freeze_cash", 0))
            return self._send({"ok": True, "asset": {
                "cash": cash, "market_value": market_value, "total_assets": total,
                "frozen": frozen, "available": cash - frozen}})
        except Exception as e:
            return self._send({"ok": False, "message": "查询资产失败: %s" % e})

    # ---- 成交回报（增量，按 seq 过滤） ----
    def _handle_fills(self, path):
        try:
            after = 0
            if "after=" in path:
                after = int(path.split("after=")[1].split("&")[0])
            new = [f for f in _fills if f["seq"] > after]
            next_seq = new[-1]["seq"] if new else after
            return self._send({"ok": True, "next_seq": next_seq, "fills": new})
        except Exception as e:
            return self._send({"ok": False, "message": "查询成交失败: %s" % e})


def _start_server():
    if HTTPServer is None:
        return
    cfg = _load_config()
    host, port = cfg.get("host", "127.0.0.1"), int(cfg.get("port", 8891))
    try:
        srv = HTTPServer((host, port), _QuantBotHandler)
        threading.Thread(target=srv.serve_forever, daemon=True).start()
    except Exception as e:
        print("QuantBot bridge start failed: %s" % e)


# ==================== Ptrade 策略入口 ====================
def initialize(context):
    _start_server()


def handle_data(context, data):
    _collect_fills()
