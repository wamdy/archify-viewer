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

## 安全边界

- 只监听 `127.0.0.1`（默认随机空闲端口）。`-addr` 填非回环地址**直接拒绝启动**——
  本工具没有任何鉴权，而 archify 产物常带真实仓库名、文件路径、commit SHA 与内部架构。
- 只认 ID 寻址：`/a/<id>` 查的是索引期建好的白名单，URL 里的 id 不参与任何路径拼接。
- 不提供服务目录列举，不透传任意路径，所有响应 `Cache-Control: no-store`。
- 不改写产物：任何写操作都不存在，测试用 sha256 全量比对钉住这条。
  （原因：archify 的 provenance sidecar 与 HTML 字节绑定，改写后 `archify check` 会对你自己的产物判红。）
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

架构、约定与红线见 [AGENTS.md](AGENTS.md)。需求与裁决记录在 appollo 主仓票 #1087。
