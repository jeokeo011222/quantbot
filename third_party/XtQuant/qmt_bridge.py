# -*- coding: utf-8 -*-
"""
QuantBot QMT 桥接策略
运行于迅投 QMT 客户端「策略交易」面板的内置 Python 引擎中，提供本地 HTTP 网关，
供 QuantBot 宿主（Go）下发订单 / 查询持仓资金 / 拉取成交回报。

无需 miniQMT / 外部 XtQuant 库：本脚本使用 QMT 客户端内置的交易 API。

用法：
  1. 打开 QMT 客户端 → 策略交易 → 新建策略（Python）
  2. 把本脚本内容整体粘贴到策略编辑器并保存
  3. 确认同目录 config.json 的 port（默认 8892）与 account（资金账号）
  4. 启动策略；QuantBot 中执行「测试连接」即完成对接

独立运行（无 QMT 客户端环境，联调用）：
  python qmt_bridge.py --mock [--port 8892]
"""
import json
import os
import sys
import threading
import time

try:
    from http.server import BaseHTTPRequestHandler, HTTPServer
except Exception:
    HTTPServer = None

# ==================== 可调参数 ====================
# 价格类型：不同 QMT 客户端版本取值可能不同，可在客户端内集中调整。
ORDER_STYLE_LIMIT = 1   # 限价单价格类型
ORDER_STYLE_MARKET = 5  # 市价（对手价）单价格类型（0 表示以最新价成交）

# ==================== 配置加载 ====================
_CONFIG_PATH = os.path.join(os.path.dirname(os.path.abspath(__file__)), "config.json")
_config = {"host": "127.0.0.1", "port": 8892, "account": "", "poll_interval": 2}


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
_seen_keys = set()   # 已处理的成交去重键


def _record_fill(order_id, symbol, side, volume, price):
    global _fill_seq
    key = (order_id, symbol, side, volume, price)
    if key in _seen_keys:
        return
    _seen_keys.add(key)
    _fill_seq += 1
    _fills.append({"seq": _fill_seq, "order_id": str(order_id), "symbol": symbol,
                   "side": side, "volume": volume, "price": price,
                   "traded_at": time.strftime("%Y-%m-%d %H:%M:%S")})
    if len(_fills) > 2000:
        _fills[: len(_fills) - 2000] = []
    return _fill_seq


def _field(obj, name, default=0):
    """兼容「对象属性」与「dict」两种形态取字段值（QMT 各版本返回形态可能不同）。"""
    try:
        if isinstance(obj, dict):
            return obj.get(name, default)
        return getattr(obj, name, default)
    except Exception:
        return default


# ==================== 成交收集 ====================
def _collect_fills():
    """handlebar 每周期调用：从 QMT 客户端内成交明细拉取新成交（防御式，API 缺失时静默跳过）。"""
    try:
        accountid = _config.get("account", "")
        if not accountid:
            return
        deals = get_trade_detail_data(accountid, "ACCOUNT", "DEAL")
        for d in deals:
            sym = _field(d, "m_dInstrumentID", "")
            if not sym:
                continue
            oid = _field(d, "m_strOrderSysID", "") or _field(d, "m_dOrderID", "")
            if not oid:
                continue
            side = _deal_side(d)
            vol = _field(d, "m_dVolume", 0)
            price = _field(d, "m_dPrice", 0)
            _record_fill(oid, sym, side, vol, price)
    except Exception:
        pass


def _deal_side(deal):
    """由成交方向字段判定 BUY/SELL（各版本字段名不同，逐个尝试）。
    m_strOffsetFlag / m_dOffsetFlag 常见取值：0=买 1=卖（部分版本为 B/S 或 买/卖）。"""
    flag = _field(deal, "m_strOffsetFlag", _field(deal, "m_dOffsetFlag", ""))
    s = str(flag).strip().upper()
    if s in ("0", "B", "BUY", "买"):
        return "BUY"
    return "SELL"


# ==================== 符号归一化 ====================
def _to_qmt_symbol(symbol):
    """把 sh600519 / 600519 归一化为 QMT 客户端使用的 600519.SH（Go 侧一般已转换，此处兜底）。"""
    sym = str(symbol).strip()
    if "." in sym:
        return sym
    low = sym.lower()
    if low.startswith("sh"):
        return sym[2:] + ".SH"
    if low.startswith("sz"):
        return sym[2:] + ".SZ"
    if low.startswith("bj"):
        return sym[2:] + ".BJ"
    if len(sym) == 6:
        if sym.startswith(("60", "68", "51", "58", "9")):
            return sym + ".SH"
        return sym + ".SZ"
    return sym


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
                               "message": "QMT 桥接策略运行中"})
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
        symbol = _to_qmt_symbol(req.get("symbol", ""))
        volume = int(req.get("volume") or 0)
        price = float(req.get("price") or 0)
        if action not in ("buy", "sell") or not symbol or volume <= 0:
            return self._send({"ok": False, "message": "参数缺失：需 action(buy/sell)+symbol+volume"})
        try:
            # 限价：price>0 用 ORDER_STYLE_LIMIT；否则市价（对手价）。买入/卖出由 action 决定。
            style = ORDER_STYLE_LIMIT if price > 0 else ORDER_STYLE_MARKET
            oid = order_shares(symbol, volume, price, style)
            return self._send({"ok": True, "order_id": str(oid)})
        except Exception as e:
            return self._send({"ok": False, "message": "下单失败: %s" % e})

    # ---- 撤单 ----
    def _handle_cancel(self, req):
        oid = req.get("order_id", "")
        symbol = _to_qmt_symbol(req.get("symbol", ""))
        if not oid:
            return self._send({"ok": False, "message": "缺少 order_id"})
        try:
            order_cancel(symbol, str(oid))
            return self._send({"ok": True})
        except Exception as e:
            return self._send({"ok": False, "message": "撤单失败: %s" % e})

    # ---- 持仓 ----
    def _handle_positions(self):
        try:
            accountid = _config.get("account", "")
            positions = []
            for p in get_trade_detail_data(accountid, "ACCOUNT", "POSITION"):
                sym = _field(p, "m_dInstrumentID", "")
                if not sym:
                    continue
                positions.append({
                    "symbol": sym,
                    "quantity": _field(p, "m_dVolume", 0),
                    "available": _field(p, "m_dCanUseVolume", 0),
                    "cost_price": _field(p, "m_dOpenPrice", 0),
                })
            return self._send({"ok": True, "positions": positions})
        except Exception as e:
            return self._send({"ok": False, "message": "查询持仓失败: %s" % e})

    # ---- 资金 ----
    def _handle_asset(self):
        try:
            accountid = _config.get("account", "")
            a = get_total_asset(accountid)
            cash = _field(a, "cash", 0)
            market_value = _field(a, "market_value", 0)
            total = _field(a, "total_asset", cash + market_value)
            frozen = _field(a, "frozen_cash", 0)
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
    host, port = cfg.get("host", "127.0.0.1"), int(cfg.get("port", 8892))
    try:
        srv = HTTPServer((host, port), _QuantBotHandler)
        threading.Thread(target=srv.serve_forever, daemon=True).start()
    except Exception as e:
        print("QuantBot QMT bridge start failed: %s" % e)


# ==================== QMT 策略入口 ====================
def init(ContextInfo):
    _start_server()


def handlebar(ContextInfo):
    _collect_fills()


# ==================== Mock 模式（无 QMT 环境联调用） ====================
def _has_qmt_api():
    try:
        get_trade_detail_data
        order_shares
        return True
    except NameError:
        return False


def _run_mock(port):
    """独立 mock 网关：固定持仓/资金，/order 生成新成交（与 scripts/mock_ptrade_bridge.py 行为一致）。"""
    global _config
    _config.update({"host": "127.0.0.1", "port": port})
    fills = _fills
    seq = [0]

    class MockHandler(BaseHTTPRequestHandler):
        def log_message(self, *a):
            pass

        def send_json(self, obj):
            body = json.dumps(obj, ensure_ascii=False).encode("utf-8")
            self.send_response(200)
            self.send_header("Content-Type", "application/json; charset=utf-8")
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            self.wfile.write(body)

        def do_GET(self):
            path = self.path.split("?")[0]
            if path == "/status":
                return self.send_json({"ok": True, "connected": True, "account": "MOCK",
                                       "message": "QMT 桥接策略运行中(MOCK)"})
            if path == "/positions":
                return self.send_json({"ok": True, "positions": [
                    {"symbol": "600519.SH", "quantity": 100, "available": 100, "cost_price": 1500.0}]})
            if path == "/asset":
                return self.send_json({"ok": True, "asset": {
                    "cash": 100000.0, "market_value": 150000.0, "total_assets": 250000.0,
                    "frozen": 0.0, "available": 100000.0}})
            if path == "/fills":
                after = int(self.path.split("after=")[1]) if "after=" in self.path else 0
                new = [f for f in fills if f["seq"] > after]
                return self.send_json({"ok": True, "next_seq": new[-1]["seq"] if new else after, "fills": new})
            return self.send_json({"ok": False, "message": "unknown: %s" % path})

        def do_POST(self):
            path = self.path.split("?")[0]
            try:
                req = json.loads(self.rfile.read(int(self.headers.get("Content-Length", 0))) or b"{}")
            except Exception:
                req = {}
            if path == "/order":
                seq[0] += 1
                oid = "MOCK-%d" % int(time.time() * 1000)
                fills.append({"seq": seq[0], "order_id": oid, "symbol": req.get("symbol", ""),
                              "side": "BUY" if req.get("action") == "buy" else "SELL",
                              "volume": req.get("volume", 0), "price": req.get("price", 10.0),
                              "traded_at": time.strftime("%Y-%m-%d %H:%M:%S")})
                return self.send_json({"ok": True, "order_id": oid})
            if path == "/cancel":
                return self.send_json({"ok": True})
            return self.send_json({"ok": False, "message": "unknown: %s" % path})

    print("QuantBot QMT bridge MOCK listening on 127.0.0.1:%d" % port)
    HTTPServer(("127.0.0.1", port), MockHandler).serve_forever()


if __name__ == "__main__":
    # 独立运行：无 QMT 客户端环境（内置 API 缺失）或带 --mock 参数时启动 mock 网关；
    # 在 QMT 客户端内运行时请以 init / handlebar 作为策略入口。
    if "--mock" in sys.argv or not _has_qmt_api():
        port = 8892
        for i, arg in enumerate(sys.argv):
            if arg == "--port" and i + 1 < len(sys.argv):
                try:
                    port = int(sys.argv[i + 1])
                except Exception:
                    pass
        _run_mock(port)
    else:
        print("QuantBot QMT 桥接策略：请在 QMT 客户端「策略交易」面板新建策略并粘贴本脚本运行")
