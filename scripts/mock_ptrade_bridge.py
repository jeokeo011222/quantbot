# -*- coding: utf-8 -*-
"""QuantBot Ptrade 桥接网关 Mock（无真实 Ptrade 环境时联调用）。
运行：python mock_ptrade_bridge.py [port]   （默认 8891）
行为：/status /positions /asset 返回固定数据；/fills 按 seq 递增返回模拟成交；
      /order 记录一笔新成交；/cancel 返回成功。
"""
import json
import sys
import time
from http.server import BaseHTTPRequestHandler, HTTPServer

PORT = int(sys.argv[1]) if len(sys.argv) > 1 else 8891

fills = []
seq = 0


class H(BaseHTTPRequestHandler):
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
                                   "message": "Ptrade 桥接策略运行中(MOCK)"})
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
            global seq
            seq += 1
            oid = "MOCK-%d" % int(time.time() * 1000)
            fills.append({"seq": seq, "order_id": oid, "symbol": req.get("symbol", ""),
                          "side": "BUY" if req.get("action") == "buy" else "SELL",
                          "volume": req.get("volume", 0), "price": req.get("price", 10.0),
                          "traded_at": time.strftime("%Y-%m-%d %H:%M:%S")})
            return self.send_json({"ok": True, "order_id": oid})
        if path == "/cancel":
            return self.send_json({"ok": True})
        return self.send_json({"ok": False, "message": "unknown: %s" % path})


if __name__ == "__main__":
    HTTPServer(("127.0.0.1", PORT), H).serve_forever()
