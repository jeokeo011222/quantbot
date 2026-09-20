# agent.dll 决策脑 — 接口说明与编译调用指南

`agent.dll` 是 QuantBot 的**决策脑二进制**（含 5 Agent 协作、多空辩论、Kelly 缩放、投资规划与每日决策循环）。源码为闭源；本仓库只发布 DLL：

- 最终用户拿到 **开源宿主仓库 + 本 DLL**，即可编译出完整可用的 exe。
- 决策脑核心（`internal/brain` 的 `agents / llm / planner / port / toolkit`）已编译进 DLL，仓库不含这些源码。

---

## 1. 文件说明

| 文件 | 用途 |
|------|------|
| `agent.dll` | 决策脑二进制（Windows x64 / c-shared）。随 Release 单独发布，也内置在发布 zip 内 |
| `bin/agent.h` | C ABI 头文件（cgo 自动生成）。宿主或外部程序据此调用 DLL 导出函数 |

> 头文件与 DLL 必须**同版本配套**使用（`AgentAbi()` 返回的 `abi` 为 `"3"`，宿主可校验）。

---

## 2. 导出函数（C ABI）

| 函数 | 签名 | 说明 |
|------|------|------|
| `AgentVersion` | `char* (void)` | 返回 `{ok,error,data:{version,abi}}` |
| `AgentPing` | `char* (char* msg)` | 联通自检，回显入参 |
| `AgentAbi` | `char* (void)` | 返回完整 ABI 清单：`{abi,api_version,brain,operations}`（含本次编译进的决策脑子包） |
| `AgentInit` | `char* (char* configJSON)` | 建立一次决策脑会话，装配团队，返回不透明 `handle:int64` |
| `AgentRunDailyCycle` | `char* (int64 handleID, char* runJSON)` | 后台启动一次每日决策循环，立即返回 `status:"started"` |
| `AgentPoll` | `char* (int64 handleID)` | 拉取会话状态：`done / error / awaiting_request / running` |
| `AgentRespond` | `char* (int64 handleID, char* id, char* resultJSON)` | 把宿主兑现的工具/存储/上下文结果推回被阻塞的决策脑 |
| `AgentClose` | `char* (int64 handleID)` | 取消并释放会话句柄 |
| `AgentFree` | `void (char* p)` | 释放本 DLL 分配的字符串内存 |

所有返回的 `char*` 都是 JSON 字符串，外层统一封装为：

```jsonc
{ "ok": true, "error": "", "data": { ... } }
```

调用方读完结果后，必须调用 `AgentFree(p)` 释放每个非 nil 返回值。

---

## 3. 拉模式 RPC 总线

决策脑在 DLL 内自主运行，宿主通过**拉模式（polling）总线**兑现外部资源：

```
 AgentInit(configJSON) ──► handle
 AgentRunDailyCycle(handle, {date}) ──► status:"started"
        │
        ▼  (决策脑内部发起工具/存储/上下文请求，随即阻塞等待宿主兑现)
 loop:
   AgentPoll(handle) ──► status:"awaiting_request" + request{id,kind,payload}
                             │
                        AgentRespond(handle, id, resultJSON)  ◄── 宿主完成兑现后推回
   AgentPoll(handle) ──► status:"running" / "done" / "error"
 AgentClose(handle)   ──► 释放会话
```

- **request 结构**：`{id, kind, payload}`，其中 `kind ∈ "tool"|"store"|"context"`。
- **result**：宿主把 `payload` 对应的真实执行结果编码为 JSON 字符串，经 `AgentRespond('id', resultJSON)` 推回。
- **LLM 不在总线上**：LLM 由决策脑自持，配置经 `AgentInit.config.llm` 传入、DLL 内自建客户端。

---

## 4. AgentInit 请求 JSON

```jsonc
{
  "config": {
    "market": "sh000300",          // 可选，基准市场/指数
    "date": "2026-09-20",          // 可选，目标交易日；空=自动取当天
    "timeout_minutes": 10,         // 可选，运行超时（分钟）
    "llm": {
      "provider": "deepseek",      // 可选：deepseek / openai / custom
      "api_key": "sk-xxx",         // 明文 API key（ProviderDefaults 时按 provider 取默认）
      "base_url": "https://...",   // 可选，空则按 provider 取默认
      "model": "deepseek-chat"     // 可选，空则按 provider 取默认
    }
  },
  "catalog": [                     // 角色→工具目录，用于装配决策脑团队
    {
      "role": "cio",
      "tools": [
        { "name": "get_market_state", "description": "...", "parameters": { "type": "object", "properties": {} } }
      ]
    }
  ]
}
```

响应 `data`：

```jsonc
{ "handle": 1, "config": { ... }, "wired": true, "abi": "3" }
```

---

## 5. 编译与调用（宿主侧, Go）

### 5.1 加载 DLL

`internal/toolworker/agent.go` 提供跨平台加载器（`loader_windows.go` 用 `syscall.NewLazyDLL` 解析 ABI 符号）：

```go
agent, err := toolworker.Load("agent.dll")   // 返回 *Agent，含各导出函数的 Go 封装
if err != nil { /* 回退 harness 直连 */ }
defer agent.Close()
```

> 也提供 `QUANTBOT_DLL_PATH` 环境变量显式指定 DLL 路径；默认优先取**可执行文件同目录**的 `agent.dll`，其次取仓库布局的 `bin/agent.dll`。

### 5.2 装配一次会话并运行每日决策

```go
host := &toolworker.Host{}
host.BindReal(
    tools,                                   // map[string]port.ToolExecutor ：真实工具
    store,                                   // port.Persistence ：真实持久化
    ctxProvider,                             // port.ContextProvider ：真实系统上下文
)

catalog := toolworker.BuildCatalog(toolsByRole)   // 由角色→工具构建 AgentInit.catalog

llmCfg := port.LLMConfig{
    Provider: cfg.AIProvider,
    APIKey:   cfg.AIAPIKey,
    BaseURL:  cfg.AIBaseURL,
    Model:    cfg.AIModel,
}

result, counters, err := toolworker.RunAgentCycle(agent, host, catalog, date, llmCfg, 4*time.Minute)
```

### 5.3 C 程序调用示例（配合 `bin/agent.h`）

```cpp
#include "agent.h"
#include <stdio.h>
#include <stdlib.h>

int main() {
    char* v = AgentVersion();                 
    printf("%s\n", v);                       
    AgentFree(v);                             // 必须释放

    const char* cfg = "{\"config\":{\"date\":\"2026-09-20\",\"llm\":{\"provider\":\"deepseek\",\"api_key\":\"sk-xxx\"}}}";
    char* init = AgentInit((char*)cfg);       // -> {ok,data:{handle}}
    // ... 解析 handle，随后 AgentRunDailyCycle / AgentPoll / AgentRespond 拉模式轮询 ...
    // ... 结束后 AgentClose(handle)
    return 0;
}
```

---

## 6. 从源码构建 exe（最终用户）

前提：安装 **Go 1.22+**（cgo 依赖 gcc；Windows 建议用 `mingw-w64`）。仓库不含 `internal/brain`，也不需要在本地编译 DLL。

```bash
# 1) 把 agent.dll 放到项目根 bin/ 目录
mkdir -p bin && cp /path/to/agent.dll bin/

# 2) 构建（wails, 产物含 exe + 自动拷贝的 agent.dll + manifest）
./build.ps1
# 或原生构建宿主（非 wails）：go build ./...

# 3) 运行
./build/bin/QuantBot.exe
```

> 若不把 DLL 放到 exe 同目录，`dailyCycleUseDLL()` 会回退到进程内 harness 直连（决策能力降级但不崩溃）。放置 DLL 后即启用完整的 DLL 决策路径。

---

## 7. 版本与兼容

- `AgentAbi()` 返回 `abi:"3"`、`api_version:"1.0.0"`、`brain:["agents","llm","port","toolkit"]`。
- 宿主加载时应调用 `AgentAbi()` 校验 `abi` 一致；不匹配时**不得**继续调用其余导出函数。
- 每次发布 DLL 会同时更新 Release 资产与发布 zip 内的 `agent.dll`，下载时以最新 Release 为准。