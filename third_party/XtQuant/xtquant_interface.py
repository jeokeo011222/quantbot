# -*- coding: utf-8 -*-
"""
XtQuant 实盘交易接口脚本
对接迅投 QMT (XtQuant) 交易接口，提供连接、查询与下单能力。

用法：
    python xtquant_interface.py --action connect
    python xtquant_interface.py --action query_asset
    python xtquant_interface.py --action buy --symbol 600519 --volume 100 --price 1500.0
    python xtquant_interface.py --action sell --symbol 600519 --volume 100 --price 1500.0
"""
import argparse
import json
import os
import sys
import time

try:
    from xtquant.xttrader import XtQuantTrader, XtQuantTraderCallback
    from xtquant.xttype import StockAccount
except ImportError:
    print(json.dumps({"status": "error", "message": "未安装 XtQuant 库，请执行: pip install xtquant"}))
    sys.exit(1)


def load_config():
    cfg_path = os.path.join(os.path.dirname(os.path.abspath(__file__)), "config.json")
    with open(cfg_path, "r", encoding="utf-8") as f:
        return json.load(f)


class TraderCallback(XtQuantTraderCallback):
    def on_disconnected(self):
        print(json.dumps({"status": "disconnected"}))

    def on_stock_order(self, order):
        print(json.dumps({"status": "order", "order_id": order.order_id, "stock": order.stock_code,
                          "volume": order.order_volume, "price": order.price, "status": order.order_status}))

    def on_stock_trade(self, trade):
        print(json.dumps({"status": "trade", "order_id": trade.order_id, "stock": trade.stock_code,
                          "volume": trade.traded_volume, "price": trade.traded_price}))


def create_trader(cfg):
    path = cfg.get("path", "")
    if not path:
        raise ValueError("未配置 XtQuant 路径")
    session_id = int(time.time() * 1000) % 100000
    trader = XtQuantTrader(path, session_id)
    callback = TraderCallback()
    trader.register_callback(callback)
    trader.start()
    connect_result = trader.connect()
    if connect_result != 0:
        raise ConnectionError("QMT 连接失败，请确认 QMT 客户端已登录")
    account = StockAccount(cfg.get("account", ""), cfg.get("account_type", "STOCK"))
    trader.subscribe(account)
    return trader, account


# ==================== 常驻网关模式（--serve） ====================
# 保持单一 XtQuantTrader 长连接，通过 stdin 读 JSON 命令、stdout 写 JSON 响应，
# 异步成交/委托回报以 {"type":"event",...} 行推送。供 Go 侧 internal/broker 驱动。
_g_state = {"trader": None, "account": None, "connected": False}


def _emit(obj):
    try:
        sys.stdout.write(json.dumps(obj, ensure_ascii=False) + "\n")
        sys.stdout.flush()
    except Exception:
        pass


# 成交侧实时回调：on_disconnected/on_stock_order/on_stock_trade 推送事件
class ServeCallback(XtQuantTraderCallback):
    def on_disconnected(self):
        _g_state["connected"] = False
        _emit({"type": "event", "event": "disconnected"})

    def on_stock_order(self, order):
        _emit({"type": "event", "event": "order", "order_id": getattr(order, "order_id", ""),
               "symbol": getattr(order, "stock_code", ""), "volume": getattr(order, "order_volume", 0),
               "price": getattr(order, "price", 0), "status": getattr(order, "order_status", "")})

    def on_stock_trade(self, trade):
        ot = getattr(trade, "order_type", 23)
        _emit({"type": "event", "event": "trade", "order_id": getattr(trade, "order_id", ""),
               "symbol": getattr(trade, "stock_code", ""), "side": "BUY" if ot == 23 else "SELL",
               "volume": getattr(trade, "traded_volume", 0), "price": getattr(trade, "traded_price", 0)})


def _get_trader(cfg):
    if _g_state.get("trader") and _g_state.get("connected"):
        return _g_state["trader"], _g_state["account"]
    path = cfg.get("path", "")
    if not path:
        raise ValueError("未配置 XtQuant 路径")
    session_id = int(time.time() * 1000) % 100000
    trader = XtQuantTrader(path, session_id)
    trader.register_callback(ServeCallback())
    trader.start()
    res = trader.connect()
    if res != 0:
        _g_state["connected"] = False
        raise ConnectionError("QMT 连接失败，请确认 QMT/MiniQMT 客户端已登录")
    account = StockAccount(cfg.get("account", ""), cfg.get("account_type", "STOCK"))
    trader.subscribe(account)
    _g_state["trader"], _g_state["account"], _g_state["connected"] = trader, account, True
    return trader, account


def _asset_dict(asset):
    return {
        "cash": getattr(asset, "cash", 0),
        "market_value": getattr(asset, "market_value", 0),
        "total_assets": getattr(asset, "total_assets", getattr(asset, "cash", 0) + getattr(asset, "market_value", 0)),
        "frozen": getattr(asset, "frozen_cash", 0),
        "available": getattr(asset, "cash", 0),
    }


def _handle(cmd, req):
    cfg = load_config()
    if cmd in ("connect", "asset"):
        trader, account = _get_trader(cfg)
        asset = trader.query_stock_asset(account)
        return {"asset": _asset_dict(asset)}
    if cmd == "position":
        trader, account = _get_trader(cfg)
        poss = trader.query_stock_positions(account)
        return {"positions": [
            {"symbol": p.stock_code, "quantity": p.volume,
             "available": getattr(p, "can_use_volume", 0), "cost_price": p.open_price, "market": ""}
            for p in poss]}
    if cmd in ("buy", "sell"):
        order_type = 23 if cmd == "buy" else 24   # 23=买入 24=卖出
        price_type = 11 if (req.get("price") or 0) <= 0 else 14  # 11=最新价 14=限价
        sym = req.get("symbol", "")
        vol = int(req.get("volume") or 0)
        if not sym or vol <= 0:
            raise ValueError("缺少 symbol/volume")
        trader, account = _get_trader(cfg)
        oid = trader.order_stock(account, sym, order_type, vol, price_type,
                                 float(req.get("price") or 0), req.get("strategy", "QuantBot"), "QuantBot")
        return {"order_id": str(oid)}
    if cmd == "ping":
        return {"pong": True}
    raise ValueError("未知命令: %s" % cmd)


def serve(cfg):
    """常驻网关入口：逐行读取 stdin 命令，处理后写回 stdout。"""
    try:
        _get_trader(cfg)
    except Exception as e:
        _emit({"type": "event", "event": "disconnected", "message": str(e)})
    for line in sys.stdin:
        line = line.strip()
        if not line:
            continue
        rid, cmd = None, None
        try:
            req = json.loads(line)
            rid, cmd = req.get("id"), req.get("cmd")
            out = dict(_handle(cmd, req))
            out.update({"id": rid, "type": "response", "ok": True, "cmd": cmd})
            _emit(out)
        except Exception as e:
            _emit({"id": rid, "type": "response", "ok": False, "cmd": cmd, "message": str(e)})


def main():
    parser = argparse.ArgumentParser(description="XtQuant 交易接口")
    parser.add_argument("--action", choices=["connect", "query_asset", "query_position", "buy", "sell", "serve"])
    parser.add_argument("--symbol", default="")
    parser.add_argument("--volume", type=int, default=0)
    parser.add_argument("--price", type=float, default=0.0)
    parser.add_argument("--strategy", default="QuantBot")
    args = parser.parse_args()

    cfg = load_config()

    if args.action == "serve":
        serve(cfg)
        return

    try:
        if args.action == "connect":
            trader, account = create_trader(cfg)
            asset = trader.query_stock_asset(account)
            print(json.dumps({"status": "connected", "cash": getattr(asset, "cash", 0),
                              "market_value": getattr(asset, "market_value", 0)}))
            trader.stop()
        elif args.action == "query_asset":
            trader, account = create_trader(cfg)
            asset = trader.query_stock_asset(account)
            print(json.dumps({"status": "ok", "cash": getattr(asset, "cash", 0),
                              "market_value": getattr(asset, "market_value", 0)}))
            trader.stop()
        elif args.action == "query_position":
            trader, account = create_trader(cfg)
            positions = trader.query_stock_positions(account)
            result = [{"stock": p.stock_code, "volume": p.volume, "available": p.can_use_volume,
                       "cost": p.open_price} for p in positions]
            print(json.dumps({"status": "ok", "positions": result}))
            trader.stop()
        elif args.action in ("buy", "sell"):
            trader, account = create_trader(cfg)
            order_type = 23 if args.action == "buy" else 24  # 23=买, 24=卖
            price_type = 11 if args.price <= 0 else 14  # 11=最新价, 14=限价
            order_id = trader.order_stock(account, args.symbol, order_type, args.volume,
                                          price_type, args.price, args.strategy, "QuantBot")
            print(json.dumps({"status": "submitted", "order_id": order_id}))
            trader.stop()
    except Exception as e:
        print(json.dumps({"status": "error", "message": str(e)}))
        sys.exit(1)


if __name__ == "__main__":
    main()
