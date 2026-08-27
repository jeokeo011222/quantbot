# QuantBot 前端

QuantBot（AI 量化机器人）的桌面前端源码。基于 **React + TypeScript + Vite** 构建，通过 **Wails** 与 Go 后端通信，提供 A 股量化交易的图形界面：市场总览、多因子选股、AI 智能体决策、投资组合、策略回测、实时活动、审计日志等。

> 本项目仅开源**前端**部分，后端（Go 业务逻辑）闭源，以 `QuantBot.exe` 形式分发。
> 前端通过 Wails 自动生成的绑定（`src/wailsjs/`）调用后端，**无法脱离后端独立运行**。

## 技术栈

- [React 18](https://react.dev/) + [TypeScript](https://www.typescriptlang.org/)
- [Vite 5](https://vitejs.dev/) + [Tailwind CSS](https://tailwindcss.com/)
- [Zustand](https://zustand-demo.pmnd.rs/)（状态管理）
- [Recharts](https://recharts.org/)（图表）
- [lucide-react](https://lucide.dev/)（图标）

## 目录结构

```
ui/
├── src/
│   ├── components/   # 通用组件（Logo、标题栏、布局、主题等）
│   ├── pages/        # 页面（看板、选股、CIO、组合、策略、设置等）
│   ├── services/     # 后端 API 调用封装
│   ├── store/        # Zustand 状态
│   ├── i18n/         # 中英文翻译
│   ├── hooks/        # 自定义 Hooks
│   ├── utils/        # 工具函数
│   ├── types/        # 类型定义
│   ├── App.tsx       # 应用入口组件
│   └── main.tsx      # 启动入口
├── public/           # 静态资源（favicon 等）
├── img/              # Logo 图片
├── wailsjs/          # Wails 自动生成的后端绑定（勿手动修改）
├── index.html
├── package.json
├── vite.config.ts
└── tsconfig.json
```

## 如何运行

前端必须配合 Wails 后端一起运行，推荐在项目根目录（含 Go 后端）使用 Wails 命令：

```bash
# 开发模式（需后端源码）
wails dev

# 构建桌面应用（生成 QuantBot.exe，需后端源码）
wails build
```

仅构建前端静态产物（不含后端，无法独立运行）：

```bash
npm install
npm run build
# 产物输出到 build/
```

## 注意

- `src/wailsjs/` 下的绑定文件由 Wails 自动生成，与后端方法签名严格对应，请勿手工修改；后端接口变更后需重新执行 `wails build` 或 `wails generate module` 刷新。
- 前端不包含任何业务数据与密钥；API Key 等敏感信息仅由用户在运行时输入并存储于后端本地。

## 许可证

[Apache License 2.0](LICENSE)
