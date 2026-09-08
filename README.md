<div align="center">

# 🤖 QuantBot

### 一个真正会每天工作的 A 股量化交易系统

**AI × Quant × Risk × Portfolio × Trading**

不是聊天机器人，不是简单选股器。
而是一套从 **研究 → 策略 → 回测 → 风控 → 组合 → 交易 → 复盘** 的完整量化投资工作流。

<br>

## 📥 立即下载

[![Download](https://img.shields.io/badge/⬇-下载_QuantBot_v1.3.0-2ea44f?style=for-the-badge&logo=github&logoColor=white)](https://github.com/jeokeo011222/quantbot/releases/download/v1.3.0/QuantBot-v1.3.0.zip)

> 下载 → 解压 → 运行 `QuantBot.exe` → 配置 API Key 即可开始。支持 Windows x64。

<br>

[![Go](https://img.shields.io/badge/Go-1.25-00ADD8?logo=go&logoColor=white)](https://go.dev)
[![Wails](https://img.shields.io/badge/Wails-v2-C2185B?logo=wails&logoColor=white)](https://wails.io)
[![React](https://img.shields.io/badge/React-18-61DAFB?logo=react&logoColor=black)](https://react.dev)
[![License](https://img.shields.io/badge/License-Proprietary-red.svg)](#-disclaimer)
[![Release](https://img.shields.io/badge/Release-v1.3.0-2ea44f)](https://github.com/jeokeo011222/quantbot/releases/download/v1.3.0/QuantBot-v1.3.0.zip)

**⭐ 如果你觉得 AI + 量化投资很酷，请给这个项目一个 Star**

[🚀 快速开始](#-快速开始) · [🧠 AI 投资团队](#-ai-投资团队) · [📊 量化能力](#-量化能力) · [🛡️ 风控体系](#️-风控体系) · [📡 数据源](#-数据源) · [🗺️ Roadmap](#️-roadmap)

</div>

---

## 🌟 QuantBot 是什么？

如果每天的投资工作是：

> 看市场 → 找机会 → 做研究 → 选股票 → 回测 → 控风险 → 管组合 → 执行交易 → 复盘

那么 QuantBot 希望把这整个过程放进一个系统。

```text
                QuantBot
                   │
       ┌───────────┼───────────┐
       ↓           ↓           ↓
   Research     Strategy      Risk
       │           │           │
       └───────────┼───────────┘
                   ↓
               Portfolio
                   ↓
                Trading
                   ↓
                Review
                   │
                   └──────→ Next Day
```

它不是让一个大模型扮演"股神"。

而是让：**AI + Quant + Risk + Portfolio + Trading**

共同组成一个每天持续工作的投资系统。

---

## ✨ 核心亮点

| | | |
|:---:|:---:|:---:|
| 🧠 **AI 投资团队**<br>5 个智能体协作决策 | 📈 **多因子选股**<br>自适应权重 + 分半稳定性检验 | 🎯 **17 种策略**<br>趋势/反转/量价/资金流全覆盖 |
| 🛡️ **三层风控**<br>规则门 + AI 评审 + 熔断 | 🧪 **量化回测**<br>成本模型 + 样本池胜率校准 | 🎁 **ETF Monitor**<br>五维共振判断机会/风险 |

---

## 🧠 AI 投资团队

QuantBot 当前采用多个 AI Agent 协作完成投资工作。

| Agent | 角色 | 主要职责 |
|:---:|---|---|
| 🧭 **Planner** | 投资规划师 | 投资目标、资金规模、风险偏好、投资约束 |
| 🔬 **Quant** | 量化分析师 | 股票池、因子分析、策略、回测、量化评分 |
| 🛡️ **Risk** | 风控师 | 风险识别、仓位约束、市场状态、组合风险 |
| 👔 **CIO** | 首席投资官 | 综合分析并形成最终投资决策 |
| 🤖 **Trader** | AI 操盘手 | 建仓、调仓、仓位调整、模拟 / QMT 实盘 |

### 5 个 Agent，不是 5 个聊天窗口。

每个 Agent 都拥有自己的职责、任务和决策边界。

```text
Planner -> Quant -> Risk -> CIO -> Trader
```

形成完整的投资决策链。

<p align="center">
  <img src="ui/image/AI团队.png" alt="AI 团队架构" width="100%">
</p>

### 💬 CIO 决策主张层：多空辩论 + Kelly 仓位缩放

盘前决策不止是规则计算，CIO 会组织 **LLM 多空辩论**：

```text
Phase1 主张 → Phase1b 强制取证（真实证据链）
   ↓
Phase2 对抗评审（质疑者挑刺，只能更保守）
   ↓
Phase3 多空辩论（看多 vs 看空论据 + 共识度）
   ↓
Phase4 Kelly 仓位缩放（置信度 × 共识度 → 缩放系数）
```

- **规则风控门**：`生效仓位 = min(规则, 主张, 异议, Kelly)` —— AI 只能让仓位**更保守**，绝不放大越线。
- **硬回退**：LLM 未配置 / 失败 / 超时 → 完全回退规则决策，AI 单点故障不阻断交易。
- **可审计**：辩论方向、多空论据、共识度、Kelly 系数全部写入决策证据链，前端可查。

---

## ⏱️ 每天自动工作的投资流程

QuantBot 不是：

```text
打开 → 问 AI 一个问题 → 关闭
```

而是一套持续运行的工作流，**单步失败不中断**——某阶段失败仅留痕，其余阶段照常推进。

### 🌅 盘前

```text
昨日复盘 -> Planner -> Quant -> Risk -> CIO -> 今日投资决策
```

系统在 A 股开盘前完成当天的投资分析与决策链。

### 📈 盘中

```text
市场变化 -> 组合监控 -> 风险状态 -> AI 分析 -> 建仓 / 加仓 / 防守
```

持续监控 ACTIVE / RUNNING 投资方案，结合**市场六维判势**实时调整。

### 🌙 盘后

```text
交易结算 -> 因子复盘 -> 策略分析 -> CIO 深度复盘 -> 制定明日计划
```

最终形成：

> **分析 → 决策 → 执行 → 结果 → 复盘 → 再决策**

的完整闭环。

<p align="center">
  <img src="ui/image/实时活动.png" alt="实时活动" width="100%">
</p>

---

## 📊 不只是 AI

QuantBot 的一个核心原则：

> **LLM 负责理解与决策协作，量化引擎负责计算事实。**

因此 QuantBot 不是：

```text
用户 -> ChatGPT -> "我觉得这只股票不错"
```

而是：

```text
投资方案 -> 量化筛选 -> 因子分析 -> 策略分析 -> 风险分析 -> 组合分析 -> AI Agent 协作 -> 投资决策
```

AI 建立在量化数据和系统状态之上，而不是凭感觉猜股票。

---

## 📈 量化能力

### 🧭 市场六维判势（MarketSixDim）

盘前决策与盘中监控共用一套**真实数据驱动的市场判势**：

```text
技术(0.15) + 广度(0.20) + 量能(0.15) + 资金(0.15) + 情绪(0.25) + 外部(0.10)
        ↓ 冲突修正 → 仓位系数
```

- ≥80 强势（0.8~1.0） · 60~79 结构性震荡（0.4~0.6） · 40~59 偏弱（0.2~0.3） · <40 退潮风险（0.0~0.1）
- 情绪优先同花顺官方涨停/跌停/炸板池，资金结构优先北向实时净流入，**严禁伪造、全部标注来源**

### 🎯 内置策略（17 种）

| 类别 | 策略 |
|---|---|
| 趋势 | 双均线、EXPMA、布林带突破、HMA 赫尔均线、**超级趋势**、DMI |
| 动量 | MTM、TRIX、捉妖大师、KDJ 金叉 |
| 反转 | RSI 反转、CCI 突破 |
| 量价 | MFI 量价反转、**CMF 资金流向**、Aroon 趋势方向 |
| 突破 | 海龟交易法 |
| AI | 机器学习（5 模型集成） |

- **样本池回测**：以持仓→基准指数构建样本池，回测聚合计算胜率，修复单一指数胜率失真。
- **盘后自动调优**：策略指标每日盘后自动刷新，可手动触发。
- **策略驱动减仓**：次日策略卖出信号（如 KDJ 死叉）优先于市场状态机执行。

### 🧪 因子复盘 + 诚实下线

- 每个因子逐日计算 **IC / RankIC / 分组表现 / 覆盖率 / 准确率 / 稳定性 / 衰减**。
- **分半稳定性检验**：横截面样本按固定种子两分，两半段 RankIC 同号且达标才算"稳定"，防止假阳性。
- **诚实下线**：分半不稳定的因子，质量分 IC 贡献减半、选股权重强力降权（≤0.3），自然退场。
- **自适应权重**：好因子上调、差因子下调（clamp [0.5,1.5]），复盘结论真正影响次日选股。

| 能力 | 说明 | 截图 |
|------|------|------|
| 🔎 **选股** | 多因子智能选股 | <img src="ui/image/选股引擎.png" width="200"> |
| 🧪 **策略** | 17 种内置策略 + 机器学习 5 模型集成 | <img src="ui/image/量化策略.png" width="200"> |
| 📊 **回测** | 收益、最大回撤、Sharpe、胜率、策略表现 | <img src="ui/image/量化回测.png" width="200"> |
| 💼 **投资组合** | 管理持仓、盈亏、资产曲线、投资方案、每日决策 | <img src="ui/image/投资方案.png" width="200"> |
| 🧠 **AI 决策** | 5 Agent 协作 + 多空辩论 + Kelly 缩放 | <img src="ui/image/投资决策.png" width="200"> |

---

## 🛡️ 风控体系

| 层级 | 机制 |
|---|---|
| **规则风控门** | AI 主张/异议仓位 clamp [0.2,1.0]，只更保守；六维判势仓位系数兜底 |
| **回撤熔断** | 触发止损熔断/回撤预警时强制防守 |
| **成本模型** | 回测买卖含手续费/印花税/市场冲击，现金不足自动回退整手 |
| **工作流容错** | 单步失败不中断；盘后主周期失败仍照常结算/复盘 |

---

## 🎁 ETF Monitor

如果你不想研究单只股票，QuantBot 里还藏了一个小功能：

它将多个市场信息进行综合：

```text
价格位置 + 份额流向 + 交易方向 + 成交额热度 + 融资杠杆 -> 共振判断
```

最终简化成：

🟢 **机会**　🟡 **观察**　🔴 **风险**

并提供 ETF 共振热力图。

> 不懂选股？　**先看看 ETF。**

<p align="center">
  <img src="ui/image/ETF监控.png" alt="ETF Monitor" width="100%">
</p>

---

## 🚀 快速开始

### 本地运行

```bash
# 1. 下载解压
# 下载地址：https://github.com/jeokeo011222/quantbot/releases/download/v1.3.0/QuantBot-v1.3.0.zip

# 2. 运行 QuantBot.exe

# 3. 配置 AI API Key（设置页）
#    支持：DeepSeek / 豆包 / 千问 / OpenAI API 兼容服务

# 4. 配置数据源（设置-数据源）
#    行情：通达信 / 腾讯财经 / 同花顺官方 / TDX MCP
```

### 从源码构建

```bash
# 后端
go build ./...

# 前端
cd ui && npm install && npm run build

# 打包（Wails）
wails build -platform windows/amd64
```

---

## 🖥️ 为什么是本地软件？

QuantBot 当前采用：

**Go + React + SQLite + DuckDB + LLM**

- 📦 下载、解压、运行，开箱即用
- 💾 核心数据与数据库保存在本地，隐私安全
- 🔑 AI 服务使用你自己的 API Key，成本可控
- ⚡ 本地 DuckDB 用于历史研究数据，毫秒级查询

---

## 📡 数据源

当前支持的数据来源包括：

| 类型 | 来源 |
|---|---|
| **实时行情** | 通达信终端 / 腾讯财经 / 同花顺官方 / TDX MCP |
| **历史研究数据** | 本地 DuckDB（全市场近 3 年日 K、复权因子、交易日历） |
| **市场信息** | 六维判势（情绪池/北向/融资/全球指数）+ 同花顺官方金融数据 |

行情读取与量化研究相互解耦，**外部实时源统一限流 + 缓存**，全部标注来源、严禁伪造。

---

## 🗺️ Roadmap

QuantBot 目前已经可以完成核心投资工作流。接下来不会无限堆功能，而是：

> **把已有能力做深。**

- [x] 多智能体投资决策链
- [x] 市场六维判势
- [x] CIO 多空辩论 + Kelly 仓位缩放
- [x] 因子分半稳定性检验
- [x] 17 种内置策略
- [x] 更完善的组合风险管理
- [ ] 更好的因子
- [ ] ETF Monitor 增强
- [ ] 社区策略生态

---

## 🌱 Open Source

QuantBot 是一个长期项目。它不是一个几十个人同时开发的大型商业产品。

很多东西来自：

> 一个人不断研究、设计、写代码、测试，然后一点点把它做出来。

最初的问题很简单：

> **"能不能让 AI 帮我做量化投资？"**

然后逐渐变成：

```text
AI Agent -> Quant -> Risk -> Portfolio -> Trading -> Daily Workflow -> Self Review
```

现在，我把它放到 GitHub。希望它能够帮助更多对：**AI × Quant × Finance** 感兴趣的人。

---

## 🤝 Community

欢迎对以下方向感兴趣的人参与交流：

- Quantitative Finance
- AI Agent
- LLM
- Algorithmic Trading
- Portfolio Management
- Risk Management
- Factor Investing
- ETF
- Open Source

如果你发现 Bug、产生新的想法，或者希望贡献代码：**欢迎提交 Issue / Pull Request。**

欢迎国内的云服务商、数据服务商给我们提供资源，联系我们：**QQ 3571038944**

---

## ⚠️ Disclaimer

QuantBot 仅供学习、研究和量化投资实验使用。

程序产生的信号、评分、策略、AI 分析、投资决策及回测结果，均基于历史数据与模型计算。

**不保证未来收益，也不构成任何投资建议。**

金融市场存在风险。

模拟交易结果不代表真实交易结果。使用 QMT 等接口进行实盘交易时，请充分理解相关风险，并遵守适用的法律法规、交易所及券商规定。

请始终只使用你能够承受损失的资金进行投资。

> 💡 程序运行过程中，需要消耗一定的 Token 成本，请根据自己的需要调整刷新频率。

---

<div align="center">

**让 AI 成为你的投资团队。**

<br>

⭐ Star · 🍴 Fork · 🐛 Issue

<br>

欢迎国内的云服务商、数据服务商给我们提供资源，联系我们：QQ 3571038944
<br>

**Made with ❤️ by QuantBot Lab**

</div>
