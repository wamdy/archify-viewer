# archify-viewer

archify 产物库的单机查看器：一个 Go 单二进制，把散落在各工作目录的 `.archify/` 产物
（架构图 / 流程图 / 时序图 / 数据流 / 生命周期 / ERD / 类图 / 树 / 时间线 / 瀑布图 / 一页解说）
汇成一个可筛选、可直接打开的本地库。

**只做四件事**：发现 → 索引 → 呈现清单 → 打开。渲染、动效、导出、深链探索全部由产物 HTML
自带的 Viewer Runtime 承担；本工具**不渲染、不校验、不改写任何产物**。

## 为什么需要它

archify 每次交付把产物写到**当时工作目录**下的 `.archify/<type>-<slug>-<YYYYMMDD-HHMMSS>/`，
换个目录就断档，日子久了没人记得图在哪儿。archify 自身只解决「一件文件」的预览
（`preview` 监视单个输入、`demo` 产一张内置样例、`deliver --open` 调系统打开器），
**库层是它明确不做的一侧**。这个工具补的就是库层。

## 安装

预编译产物在 Releases；从源码：

```powershell
powershell -File scripts/build.ps1     # → build\archify-view-windows-amd64.exe
```

Windows 产物用 `-H windowsgui` 链接成 GUI 子系统：**双击启动不弹终端黑窗**，命令行调用时
由 `console_windows.go` 附着父控制台照常打印；启动失败又没有控制台时会弹一次错误框，
不会「双击后什么都没发生」。exe / 窗口标题栏 / 托盘共用同一套图标资源
（`assets/icon.ico` + `rsrc_windows_*.syso`，见「开发」）。

## 四种用法

```bat
archify-view                                :: 双击即此：原生窗口（不可用时开浏览器）
archify-view list                           :: 终端清单，不开窗口不起服务
archify-view list --json --type erd --limit 10
archify-view open 3f2a1c9b0d4e              :: 按 ID 交给系统默认浏览器（file:// 直开）
archify-view run  --root D:/design/.archify  :: 只要服务，打印 URL，Ctrl-C 退出
```

产物根按三档优先级发现，命中即停：

1. `-root PATH`（可重复）；
2. 从当前目录**逐级向上**找 `.archify/`（只查祖先链，不扫全盘）；
3. `config.yaml` 的 `roots` 列表（见 `config.example.yaml`）。

配置文件查找顺序：当前目录 `config.yaml` → 用户配置目录 `archify-viewer/config.yaml`。

## 界面

库视图：搜索标题/slug/目录名，按类型、来源、时间/体积排序；每张卡片给出类型、交付态
（`已交付` / `交付失败` / `未记录`，来自 archify 自己的 `.delivery.json`）、体积、修改时间、
来源 label 与相对路径。卡片动作仍是三个：**查看**（在当前页面内展开原始 HTML，不弹窗口）、
**复制深链**、**复制 ID**；查看面板另留一个「在新标签打开」作为可选动作（等价于旧版「打开」）。

查看与列表在同一页面切换，地址栏 `#a/<id>` 就是当前查看状态，浏览器前进/后退与刷新深链都收敛到同一入口；
`Esc` 返回列表并恢复原滚动位置。按 `/` 聚焦搜索框（输入框内不抢占），`Esc` 可清空搜索。

首屏、重新扫描、产物载入都有进行中状态；索引读取失败会给出原因与「重试」，并标记当前列表是上次成功的数据；
「筛选无结果」与「库为空」是两种不同空态，前者显示当前条件并可一键清除。重扫、复制的结果用右下角轻提示反馈，
不使用弹窗。「重新扫描」在不重启进程的前提下重读产物根（仍然只读），进行中禁止重复点击。

只读边界不变：查看面板用内嵌框架加载 `/a/<id>` 的原始字节，本工具不解析、不校验、不改写产物；
「在新标签打开」与 `open` 命令仍走同一套 ID 白名单。

## 桌面形态与设置

顶栏的齿轮按钮（或快捷键 `,`）进入设置页，地址栏 `#settings` 也是一条可直接收藏的路由；
`Esc` 返回列表。设置页做三件事：

- **自定义 archify 目录**：增删产物根，写回 `config.yaml` 并**立即重建索引**，不用重启、不用再点一次
  「重新扫描」。目录必须存在，最多 32 处，重复路径自动拒绝。空库时卡片上的引导按钮直达这里。
  页面会标明当前来源（`flag` / `discover` / `config`）——由 `-root` 或自动发现接管时，重启后的优先级
  可能盖过配置，提示不会把「已保存」说成「永久生效」。
- **关闭行为**：`最小化到托盘`（默认）/ `每次询问` / `直接退出`。改动即时生效，托盘在每次
  `WM_CLOSE` 时重新读配置。
- **主题**：跟随系统 / 浅色 / 深色，记住在浏览器本地，首帧前就写好 `<html data-theme>`，不闪白。

Windows 原生窗口形态下，点「×」默认只隐藏窗口，系统托盘保留图标；首次隐藏会弹一次气泡说明
如何恢复。托盘右键菜单可**显示主窗口**、**重新扫描产物**、**退出**。只有托盘「退出」或设置里的
「直接退出」才会真正结束进程，且退出一律投递回 UI 线程，不留幽灵图标。系统托盘不可用时降级为
「关闭即退出」并在日志里说明，不影响查看功能。

三个设置端点只在 `gui` / `run` 形态装配；`list` / `open` 不起服务，纯只读用法不注入写面。

**原生窗口的失败兜底**：上游 `go-webview2` 在 WebView2 controller 创建失败时会对 nil 指针解引用而
panic（`pkg/edge/chromium.go` 的 `CreateCoreWebView2ControllerCompleted`）；GUI 子系统产物没有控制台，
panic 就表现为「双击后静默消失」。本工具把 `desktop.New` 包在 `newNativeWindow` 里，把 panic 收敛成
可判定 error，再走既有的系统浏览器回退路径，并用单测钉住。已在运行的实例占用 WebView2 用户数据目录时
同样按此路径处理，不会崩掉整个进程。

## 安全边界

- 只监听 `127.0.0.1`（默认随机空闲端口）。`-addr` 填非回环地址**直接拒绝启动**——
  本工具没有任何鉴权，而 archify 产物常带真实仓库名、文件路径、commit SHA 与内部架构。
- 只认 ID 寻址：`/a/<id>` 查的是索引期建好的白名单，URL 里的 id 不参与任何路径拼接。
- 不提供服务目录列举，不透传任意路径，所有响应 `Cache-Control: no-store`。
- **产物库完全只读**：扫描与 HTTP 面不出现任何写文件系统调用，测试用 sha256 全量比对钉住这条。
  （原因：archify 的 provenance sidecar 与 HTML 字节绑定，改写后 `archify check` 会对你自己的产物判红。）
  全仓唯一的写面是设置页保存用户配置 `config.yaml`（临时文件 + 原子改名 + 0600，集中在
  `internal/config.Editor`），与产物目录彻底隔离；写失败时运行态不变，不会「界面说成功、重启却回退」。
- 设置端点只收 1 MiB 以内的 JSON，路径仅作为字符串校验目录存在，**不新增任何按路径取文件的能力**；
  `/api/index` 依旧不含绝对路径。
- 默认不打印绝对路径（`list --verbose` 是唯一显式例外）。

## 开发

机械门（改动后必跑）：

```bash
gofmt -l .                                   # 必须无输出
go build ./... && CGO_ENABLED=0 go build ./...
go vet ./...
go test ./...
node --check internal/web/static/app.js
```

改了 `assets/icon.svg` 或 `assets/windows.manifest` 后要重新生成资源再构建：

```bash
go run scripts/gen-icon.go            # → assets/icon.ico、rsrc_windows_amd64.syso、rsrc_windows_arm64.syso
```

生成器只用 Go 标准库，不依赖 `rsrc` / CGO。资源 ID 是硬约定：`RT_MANIFEST=1`、
`RT_GROUP_ICON=2`（窗口 `IconId` 与托盘 `LoadImageW` 都取它）、`RT_ICON=3..6`。
`.syso` 必须随仓库提交，`go build` 才会自动把图标与 DPI 清单链进 exe。

桌面能力（纯 Win32 托盘、关闭行为三态、配置原子写、首帧主题、页面内确认）沿用
`code/tools/ssh-tunnel-ui` 已验证的做法；**没有**沿用它的非回环监听回落策略——本工具无鉴权，
非回环一律拒绝启动。

架构、约定与红线见 [AGENTS.md](AGENTS.md)。需求与裁决记录在 appollo 主仓票 #1087。
