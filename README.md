# CCBar Windows

**在 Windows 托盘查看 AI 编程工具的剩余额度、使用量和会话费用。**

CCBar 自动读取本机已登录工具的额度，以及编程工具保存的会话日志。打开统计窗口，可以按服务、模型、项目或会话查看 Tokens 与估算费用；在「Claude 账号」页面可以保存多个登录并一键切换。

本项目基于 [nanvon/cc-bar](https://github.com/nanvon/cc-bar)，使用 Go、Wails 3、React、TypeScript 和 Tailwind CSS。原版 macOS 项目由 nanvon 维护。

## 核心能力

- **额度监控**：Codex、Claude Code、Antigravity、Cursor、Command Code 的额度窗口、重置时间和服务状态
- **本地统计**：Codex、Claude Code、Pi、Oh My Pi（OMP）、OpenCode、DSH 会话日志，以及 Cursor 远端用量
- **统计分析**：服务、模型和提供商聚合，日周月图表、环比、缓存命中率和 Fast 档位费用
- **会话浏览**：全局搜索、项目筛选、分页、模型拆分和逐请求明细
- **额度历史**：真实周期、额外重置与各账号独立的额度变化时间线
- **账号管理**：多个 Claude 账号一键切换并共享会话与配置；导入额外的 Codex 账号，管理排序、显示和重置次数
- **桌面集成**：托盘弹窗、可拖动悬浮窗、登录启动、隐私模式、中英双语和明暗主题

## 系统要求

| 用途 | 需要准备 |
| --- | --- |
| 运行 CCBar | Windows 10 1709+ / Windows 11（x64 或 ARM64）与对应架构的 EXE |
| 查看服务额度 | 对应工具已在本机登录，网络能访问该服务 |
| 查看日志统计 | 对应编程工具在本机运行并保存会话记录 |
| 管理 Claude 账号 | 添加账号时完成官方授权；同步 Desktop 需要已安装 Claude Desktop，启动 CLI 需要 Claude Code CLI |
| 从源码构建 | Go 1.25+、Node.js 24、PowerShell 7 |

EXE 内置 Microsoft WebView2 固定版运行库，首次启动时解压到应用数据目录，之后直接复用。无需预装 Edge 或系统 WebView2，首次启动也不需要联网。

## 快速开始

| 电脑类型 | 下载文件 |
| --- | --- |
| Intel / AMD 的 64 位电脑 | [CCBar.exe](https://github.com/Mag1cFall/cc-bar/releases/latest/download/CCBar.exe) |
| ARM64 电脑 | [CCBar-arm64.exe](https://github.com/Mag1cFall/cc-bar/releases/latest/download/CCBar-arm64.exe) |

1. 下载对应 EXE，放在希望长期使用的目录，双击打开主窗口。
2. 首次引导中启用所需服务，选择托盘与悬浮窗的展示位置。
3. 在对应编程工具中完成登录或产生会话记录后，CCBar 会读取额度和统计。
4. 单击右下角托盘图标查看额度；双击打开统计窗口。托盘图标可能位于 Windows 的折叠区域。

左下角的月亮或太阳按钮可快速切换明暗模式。在「设置 → 外观与显示 → 主题」选择「跟随系统」「浅色」或「深色」，主窗口、托盘弹窗与悬浮窗同步切换。

关闭主窗口后，应用继续驻留托盘。完整退出时使用托盘菜单的「退出」。「登录时启动」记录的是当前 EXE 的路径，移动 EXE 后需要重新打开应用并保存一次该设置。

在「通用 → 更新」点击「检查更新」，可以检查 [Mag1cFall/cc-bar 的 Windows Release](https://github.com/Mag1cFall/cc-bar/releases)。发现新版本后点击「下载并安装」，确认后会替换当前 EXE 并重启。设置与会话记录继续保存在原数据目录。请将 EXE 放在当前用户有写入权限的目录，例如 `%LOCALAPPDATA%\Programs\CCBar`。

### Claude 多账号

打开设置的「Claude 账号」，点击「保存当前登录」保存现有登录。已安装 Desktop 时，CCBar 保存 Chat/Cowork 的网页登录和 Code 凭据，保存期间 Desktop 会短暂关闭并重新打开。首次启用 Code 时，按 Desktop 页面的提示完成连接。

点击「添加账号」直接开始登录，账号名称从邮箱自动生成，随后可重命名。已安装 Desktop 时，在 Desktop 里完成登录，CCBar 会自动连接 Code 并保存；只用 CLI 时，在系统浏览器里完成授权即可。登录进度显示在账号页面，取消后恢复此前的 Desktop 登录。

点击「切换」会把该账号设为新终端的默认账号，并让 Desktop 以该账号重新打开 Chat、Cowork 和 Code。账号完成登录并保存后才能切换。点击「启动」会打开一个只使用该账号的新终端并运行 Claude Code，默认账号和 Desktop 保持不变；命令取本机终端实际解析到的 `claude`，支持 PowerShell 函数、别名和脚本。

每个账号只单独保存登录凭据和账号状态，其余 CLI 配置共享，包括项目会话、历史、任务、计划、全局指令（CLAUDE.md）、技能、插件和设置；项目信任、全局偏好和 MCP 设置在各账号间同步。切换、启动或登录账号时检查共享链接，发现断开会修复；Claude Code 清理掉空的共享目录后，CCBar 会立即重建。切换时，Desktop Code 的本地会话列表会带到新账号，已删除的会话不会重新出现；Chat/Cowork 的云端记录显示所选账号自己的内容。已经打开的 CLI 终端仍使用打开时的账号。

Claude CLI 安装步骤见 [官方安装页面](https://code.claude.com/docs/en/setup)。在终端运行 `claude --version` 可确认安装成功。在同一工作目录运行 `claude --resume`，可以接着其他账号的会话继续。

### 统计来源

| 来源 | 默认会话位置 |
| --- | --- |
| Codex | `%USERPROFILE%\.codex\sessions` 与历史归档 |
| Claude Code | `%USERPROFILE%\.claude\projects` |
| Pi | `%USERPROFILE%\.pi\agent\sessions` |
| Oh My Pi（OMP） | `%USERPROFILE%\.omp\agent\sessions`，含配置档与子代理会话 |
| OpenCode | `%USERPROFILE%\.local\share\opencode` |
| DSH | `%USERPROFILE%\.dsh\sessions` |
| Cursor | 当前账号的远端计量记录 |

额度百分比取自服务返回的数据；费用按日志里的 Tokens 数、模型和单价估算。订阅费、剩余额度和本地估算费用分开显示。

## 数据与常见问题

CCBar 的设置、数据库和运行日志保存在 `%LOCALAPPDATA%\CCBar`。设置中的「打开数据目录」可以直接进入该位置。

| 看到的情况 | 处理方式 |
| --- | --- |
| 服务显示未检测到 | 在对应工具中完成登录，随后点击刷新 |
| 账号提示重新登录 | 在账号页面重新登录 |
| 统计列表为空 | 确认会话已产生，启用该数据来源并运行扫描 |
| 额度刷新受限 | 等服务限制时间结束，CCBar 会自动重试 |
| 切换后旧终端仍使用原账号 | 新开终端，或在账号页面点击「启动」 |
| 长会话前面的命令或子代理不显示 | Desktop 重启后只加载每个会话最后 50 MB 的记录 |
| 需要反馈问题 | 提供系统与应用版本、复现步骤，以及设置页导出的诊断包 |

## 源码构建

在项目根目录执行：

```powershell
cd web
npm ci
npm run lint
npm test
npm run build
cd ..
go install github.com/wailsapp/wails/v3/cmd/wails3@v3.0.0-beta.27
$wails = Join-Path (go env GOPATH) 'bin/wails3.exe'
New-Item -ItemType Directory -Path dist -Force
& $wails generate icons -input internal/desktop/assets/icon.png -windowsfilename dist/icon.ico -macfilename ''
& $wails generate syso -arch amd64 -icon dist/icon.ico -manifest cmd/ccbar/windows.manifest -info cmd/ccbar/version.json -out cmd/ccbar/rsrc_windows_amd64.syso
$env:CGO_ENABLED = '0'
$env:GOOS = 'windows'
$env:GOARCH = 'amd64'
Invoke-WebRequest -Uri 'https://msedge.sf.dl.delivery.mp.microsoft.com/filestreamingservice/files/0b89c3a3-0043-4746-b39e-65830da7744d/Microsoft.WebView2.FixedVersionRuntime.154.0.4258.53.x64.cab' -OutFile internal/browser/runtime.cab
go build -trimpath -tags production -ldflags '-s -w -H windowsgui' -o dist/CCBar.exe ./cmd/ccbar
```

产物为 `dist/CCBar.exe`，前端和固定版运行库已内嵌。运行库采用微软官方 Fixed Version 154.0.4258.53；其他架构的运行库可从 [Microsoft 官方下载页](https://developer.microsoft.com/microsoft-edge/webview2/) 获取。

构建 ARM64 时，将对应 ARM64 CAB 保存为 `internal/browser/runtime.cab`，资源生成参数使用 `-arch arm64 -out cmd/ccbar/rsrc_windows_arm64.syso`，设置 `$env:GOARCH = 'arm64'`，输出文件使用 `dist/CCBar-arm64.exe`。

`windows.manifest` 提供权限与 DPI 设置，`version.json` 提供文件版本和名称，二者在生成 EXE 资源时使用。

完成首次构建后，前端开发使用 Vite 热更新。在一个终端启动前端：

```powershell
cd web
npm ci
npm run dev
```

在另一个终端进入项目根目录，启动桌面宿主：

```powershell
$env:FRONTEND_DEVSERVER_URL = 'http://127.0.0.1:5173'
go run ./cmd/ccbar
```

### 代码目录

```text
cc-bar/
├── .github/
│   └── workflows/
│       └── release.yml           Windows 构建与 Release 发布
├── cmd/
│   └── ccbar/
│       ├── main.go               程序入口、日志与更新助手入口
│       ├── windows.manifest      Windows 权限与 DPI 清单
│       └── version.json          EXE 名称、版权和版本资源
├── internal/
│   ├── app/                      应用服务与界面 RPC
│   │   ├── service.go            共享状态、设置保存与额度刷新
│   │   ├── scheduler.go          定时刷新、日志监听与休眠恢复
│   │   ├── accounts.go           账号管理接口
│   │   ├── cycles.go             周期统计与剩余用量预测
│   │   ├── settings.go           设置读取与持久化
│   │   ├── locale_windows.go     Windows 系统语言读取
│   │   ├── diagnostics.go        诊断包导出与身份脱敏
│   │   ├── update.go             GitHub 更新、EXE 替换与重启
│   │   ├── cycles_test.go        周期与预测检查
│   │   ├── settings_test.go      设置读取检查
│   │   └── update_test.go        版本选择与更新流程检查
│   ├── desktop/                  原生桌面窗口与托盘
│   │   ├── desktop.go            主窗口、托盘弹窗与悬浮窗
│   │   ├── platform_windows.go   登录启动、系统事件与目录打开
│   │   ├── startup_windows.go    原生启动提示与错误显示
│   │   ├── desktop_test.go        启动方式检查
│   │   ├── icon.go               内嵌应用图标
│   │   └── assets/
│   │       └── icon.png          应用图标源文件
│   ├── browser/                  随包界面运行库
│   │   ├── runtime.go            固定运行库版本与官方来源
│   │   ├── runtime_windows.go    内嵌运行库释放与复用
│   │   ├── runtime_windows_test.go 离线释放与并发复用检查
│   │   └── runtime.cab           对应架构的微软 Fixed Version 包
│   ├── accounts/                 多账号与共享会话
│   │   ├── store.go              账号存储、登录状态与额度刷新
│   │   ├── types.go              Claude 与 Codex 账号类型
│   │   ├── cli.go                Claude 账号切换与终端启动
│   │   ├── oauth.go              浏览器回调、原生登录与进度
│   │   ├── desktop.go            Claude Desktop Code 登录导入
│   │   ├── desktop_history.go    Desktop Code 本地会话同步
│   │   ├── desktop_switch.go     Desktop 完整登录保存与切换
│   │   ├── desktop_session_windows.go 原生安装发现与会话文件
│   │   ├── platform_windows.go   PowerShell 入口、环境变量与目录联接
│   │   ├── history.go            共享配置、会话与历史，同步全局偏好与 MCP 设置
│   │   ├── codex.go              Codex 账号导入、排序与展示
│   │   ├── credits.go            Codex 重置次数管理
│   │   ├── accounts_test.go      账号隔离与历史共享检查
│   │   └── oauth_test.go         自动授权、取消与 Desktop 会话检查
│   ├── providers/                凭据发现与服务接口
│   │   ├── credentials.go        本机登录凭据读取
│   │   ├── client.go             额度请求与服务状态查询
│   │   ├── refresh.go            OAuth 凭据刷新
│   │   ├── claude_oauth.go        Claude OAuth 兑换与身份读取
│   │   ├── antigravity.go        Antigravity 本地连接与额度
│   │   ├── credits.go            Codex 限额重置接口
│   │   ├── parsers.go            服务响应解析
│   │   ├── json.go               JSON 读取与原子写入
│   │   └── providers_test.go     凭据与响应解析检查
│   ├── history/                  额度历史与账号周期
│   │   ├── history.go            额度采样与时间线
│   │   ├── cycles.go             周期边界、重置与账号使用区间
│   │   └── history_test.go       历史与周期检查
│   ├── usage/                    会话日志与用量数据库
│   │   ├── store.go              SQLite 数据库与索引
│   │   ├── scan.go               日志发现与增量扫描
│   │   ├── parse.go              Codex、Claude 与 OpenCode 日志解析
│   │   ├── harness.go            Pi、OMP 配置档与子代理会话
│   │   ├── dsh.go                DSH 日志与压缩数据解析
│   │   ├── cursor.go             Cursor 远端用量读取
│   │   ├── pricing.go            模型价格与费用计算
│   │   ├── projects.go           项目识别与会话标题
│   │   ├── query.go              概览、会话和逐请求统计
│   │   └── usage_test.go         扫描、定价与统计检查
│   ├── model/                    公共数据类型
│   │   ├── enums.go              服务、来源与额度窗口枚举
│   │   ├── types.go              设置、凭据与额度类型
│   │   └── usage.go              用量、会话与查询类型
│   └── secrets/
│       └── protect_windows.go    Windows DPAPI 凭据保护
├── web/                          React 前端
│   ├── src/
│   │   ├── main.tsx              React 入口
│   │   ├── App.tsx               页面导航与应用状态订阅
│   │   ├── Statistics.tsx        服务、日期与统计视图切换
│   │   ├── statistics/
│   │   │   ├── Overview.tsx      概览、Token 拆分与图表
│   │   │   ├── Conversations.tsx 会话搜索、分页与详情
│   │   │   ├── Timeline.tsx      额度变化时间线
│   │   │   ├── Cycles.tsx        额度周期与预测
│   │   │   └── shared.tsx        统计视图公共组件
│   │   ├── Settings.tsx          服务、外观、数据与通用设置
│   │   ├── Accounts.tsx          Claude 账号管理
│   │   ├── ResetCredits.tsx      Codex 限额重置窗口
│   │   ├── MiniWindows.tsx       托盘弹窗与悬浮窗
│   │   ├── Onboarding.tsx        首次启动引导
│   │   ├── ThemeToggle.tsx       明暗切换与圆形展开动画
│   │   ├── WindowControls.tsx    最小化、最大化与关闭按钮
│   │   ├── components.tsx       按钮、菜单、对话框与额度组件
│   │   ├── context.ts           共享应用上下文
│   │   ├── bridge.ts            Wails RPC 调用
│   │   ├── hooks.ts             查询缓存与请求取消
│   │   ├── models.ts            前端数据类型
│   │   ├── format.ts            数字、日期、服务名称与品牌色
│   │   └── styles.css           Tailwind 入口、布局与主题
│   ├── public/
│   │   ├── ccbar-icon.png       界面应用图标
│   │   └── logos/               服务 SVG 图标
│   │       ├── codex.svg
│   │       ├── claude.svg
│   │       ├── antigravity.svg
│   │       ├── cursor.svg
│   │       ├── commandcode.svg
│   │       ├── pi.svg
│   │       ├── omp.svg
│   │       ├── opencode.svg
│   │       └── dsh.svg
│   ├── embed.go                 构建后的前端资源嵌入
│   ├── index.html               页面入口
│   ├── package.json             前端依赖与开发命令
│   ├── package-lock.json        前端依赖锁定
│   ├── tsconfig.json            TypeScript 配置
│   ├── vite.config.ts           Vite 与 Tailwind 配置
│   ├── .prettierignore           格式化文件范围
│   ├── .prettierrc.json          Prettier 格式规则
│   └── eslint.config.js         ESLint 规则
├── .gitignore                   生成文件与本机数据排除
├── go.mod                       Go 模块与依赖版本
├── go.sum                       Go 依赖校验信息
├── LICENSE                      MIT 授权与作者版权
└── README.md                    使用与开发说明
```
