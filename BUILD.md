# QuantBot 构建脚本说明

本文档说明 QuantBot 仓库内的全部构建 / 开发 / 发布脚本的用途、用法与环境要求。

## 一、脚本总览

| 脚本 | 类型 | 用途 |
|------|------|------|
| `build.ps1` | PowerShell | **主构建**：wails build 产出 `QuantBot.exe`，并把决策脑 DLL 拷到产物目录 |
| `build.bat` | CMD | 与 `build.ps1` 等价的主构建（CMD 环境备用） |
| `dev.bat` | CMD | **开发模式**：`wails dev` 热重载，代码改动自动刷新 |
| `_make_release.py` | Python | **发布打包**：把 `build\bin` 打包成可分发的 ZIP + sha256 + manifest |

> 四个脚本均**不含 `-clean`**：`build\bin` 下的数据 / 配置 / 日志 / 数据库在每次构建时被完整保留，无需手动备份。

## 二、环境要求

| 依赖 | 要求 | 说明 |
|------|------|------|
| Go | 1.22+（建议 1.25） | 安装目录 `D:\go`（脚本内写死，可自行改） |
| Wails CLI | v2.14 | 安装目录 `C:\Users\JokerZ\go\bin\wails.exe`（脚本内写死） |
| Node.js | 18+（仅 `dev.bat` 需要） | 前端依赖安装用 |
| Python 3 | 标准库即可（仅 `_make_release.py` 需要） | hashlib / zipfile，无第三方依赖 |
| gcc（mingw-w64） | 构建 DLL 时需要 | cgo 依赖，Windows 建议 mingw-w64 |

> `build.ps1` / `build.bat` 使用**显式路径**（`D:\go\bin\go.exe` 等），不依赖终端 PATH。若你的安装路径不同，请修改脚本顶部的 `GO_EXE` / `WAILS_EXE`。

## 三、主构建：`build.ps1`（推荐） / `build.bat`

### 用法

```powershell
# PowerShell
.\build.ps1

# CMD / 双击
build.bat
```

### 执行流程

1. **环境检查**：验证 Go、Wails CLI 是否存在（显式路径）
2. **环境变量**：设置 `GOROOT=D:\go`、`GOPATH=C:\Users\JokerZ\go`、`PATH`
3. **构建**：`wails build -platform windows/amd64`（**无 `-clean`**，保留 `build\bin` 全部数据）
4. **验证输出**：确认 `build\bin\QuantBot.exe` 存在，打印大小与时间
5. **拷贝决策脑 DLL**（仅 `build.ps1`）：`bin\agent.dll` → `build\bin\agent.dll`

### 产物

| 路径 | 说明 |
|------|------|
| `build\bin\QuantBot.exe` | 主程序（含 Wails 前端资源） |
| `build\bin\agent.dll` | 决策脑 DLL（随 exe 分发，缺失时宿主回退 harness 直连） |
| `build\bin\agent.h` | C ABI 头文件（随 DLL 一并拷贝） |

> **DLL 构建注意**：`build.ps1` 只负责**拷贝**已有 `bin\agent.dll`。首次构建前需先编译 DLL：
>
> ```bash
> cd dll && go build -buildmode=c-shared -o ../bin/agent.dll . && cd ..
> ```
>
> 决策脑源码（`internal/brain` + `dll/`）现已全量开源，任何人可自行编译。

## 四、开发模式：`dev.bat`

`wails dev` 启动带**热重载**的开发服务器：修改 Go / 前端代码后自动重新编译并刷新界面。

### 用法与参数

```
dev.bat [options]

  -port PORT          开发服务器端口（默认 3456）
  -no-browser         不自动打开浏览器
  -skip-deps          跳过依赖检查与 npm install
  -verbose            启用详细输出
  -device DEVICE      目标设备（auto / desktop / mobile）
  -h | --help | /?    显示帮助
```

### 示例

```bat
dev.bat                              :: 默认端口启动
dev.bat -port 8080                   :: 自定义端口
dev.bat -no-browser -skip-deps       :: 快速启动（跳过依赖检查）
dev.bat -port 3456 -verbose          :: 调试模式
```

### 自动检查

- 依赖：`wails`、`npm` 是否在 PATH；`ui\node_modules` 不存在时自动 `npm install`
- 端口占用：检测到端口被占用会询问是否继续

## 五、发布打包：`_make_release.py`

把已构建的 `build\bin` 打包成可分发的 ZIP。**必须先执行 `build.ps1` 产出最新 exe 与 DLL**。

### 用法

```powershell
python _make_release.py
```

### 产物

| 路径 | 说明 |
|------|------|
| `dist\QuantBot-1.6.0.zip` | 发布包（可执行布局，含 exe / DLL / 配置 / 数据库 / 模型） |
| `dist\QuantBot-1.6.0.zip.sha256` | ZIP 的 SHA-256 校验值 |
| `build\bin\manifest.json` | 增量更新清单（exe / DLL / 模型 / stock_dict.json 的哈希） |

### 打包规则

- **包含**：`QuantBot.exe`、`agent.dll`、`用户手册.md`、`manifest.json`、`config/`、`data/`（白名单）、`database/`、`models/`
- **排除**：`data/trades/`、`data/market/`、`log/`、`reports/`（运行时 / 临时数据）
- **版本号**：脚本顶部 `VER` 变量（当前 `1.6.0`），发布前如需升版改此处

## 六、典型工作流

```text
# 完整发布流程
1. cd dll && go build -buildmode=c-shared -o ../bin/agent.dll .   # 编译决策脑 DLL
2. .\build.ps1                                                      # 构建 exe + 拷 DLL
3. python _make_release.py                                          # 打包 dist zip + sha256
4. 上传 dist\QuantBot-1.6.0.zip 与 .sha256 到 GitHub Release
```

## 七、常见问题

| 问题 | 原因 / 解决 |
|------|------------|
| `Go not found: D:\go\bin\go.exe` | Go 安装路径不同，修改脚本 `GO_EXE` 变量 |
| `Wails CLI not found` | 未安装 Wails：`go install github.com/wailsapp/wails/v2/cmd/wails@latest` |
| `agent.dll` 未拷入产物 | 仓库根 `bin\agent.dll` 不存在，先编译 DLL（见第三节） |
| `_make_release.py` 报"缺少文件" | 未先构建或文件被清，重新执行 `build.ps1` |
| 构建后数据库丢失 | 不会发生：所有脚本均不带 `-clean`，`build\bin` 数据全保留 |
