# archify-viewer — 仓库级 Agent 文档（AGENTS.md）

> 本仓是 appollo 主仓 `code/tools/` 下的独立 git 仓（submodule 托管）。进入本目录做代码探索、
> 修改或评审之前，先读本文；票据流程、文档落位与人工门控由主仓 `harness/` 体系承接，本仓不承载票据文档。

---

## 1. 仓库角色

**项目**：archify-viewer（桌面分发名 Archify Viewer）— Go 单二进制，给 archify 技能包产出的
**本地产物库**做统一浏览与快速打开。一个入口四种形态：`gui`（WebView2 原生窗口，非 Windows 或缺
运行时自动回退系统浏览器）、`run`（前台 HTTP）、`list`（终端清单，供脚本/AI 消费）、
`open <id|slug|路径>`（把单件产物交给系统默认处理程序）。无参数启动等同 `gui`。

**为什么需要它**（不是重复建设）：archify 把每次交付写到**当时工作目录**下的
`.archify/<type>-<slug>-<YYYYMMDD-HHMMSS>/`，换目录即断档；它自带的 17 个子命令里没有任何库层能力
——`preview` 只监视一个显式输入且契约禁用于 remote sharing，`demo` 只产一张内置样例 HTML。
**库层是它刻意不做的边界**（其 `references/viewer-runtime.md` 明写不要为 viewer-only 能力加托管服务）。

**范围边界（YAGNI）**：个人本地工具，不进 appollo 运行架构（无 GraphQL / DB / 服务依赖）；
**只读**产物库：不校验、不改写产物；viewer 自身**不渲染**——列表缩略图由**外部无头浏览器**
（系统 Chrome / Edge）对 `/a/{id}` 截图后落派生缓存（`%AppData%/archify-viewer/thumbs/`），
产物字节前后不变。无用户体系、无对外监听。
共享服务端（P2）与平台资产化（P3）在主仓 #1087 票面明确划在本仓之外。

---

## 2. 关键目录与分层

```
archify-viewer/
├── main.go                     # CLI 入口（gui/run/list/open/version）+ 设置编辑器与托盘装配
├── console_windows.go          # AttachConsole / AllocConsole；无控制台时启动失败弹 MessageBox
├── console_other.go
├── go.mod                      # 依赖：go-webview2、yaml.v3、x/sys/windows（纯 Win32 托盘）；winloader 间接
├── config.example.yaml
├── assets/                     # icon.svg（源）/ icon.ico（生成）/ windows.manifest（DPI + 长路径）
├── rsrc_windows_amd64.syso     # 生成：COFF .rsrc，RT_MANIFEST=1 / RT_GROUP_ICON=2 / RT_ICON=3..6
├── rsrc_windows_arm64.syso
├── scripts/
│   ├── gen-icon.go             # //go:build ignore；纯标准库生成 ICO + 两份 .syso
│   ├── build.ps1               # 本机 exe（-ldflags "-s -w -H windowsgui"）
│   └── build-dist.sh           # 三平台 + SHA256SUMS
│
├── internal/config/
│   ├── config.go               # 三档 roots 发现、ValidateAddr/IsLoopbackHost、运行时 RWMutex 快照
│   └── settings.go             # ⭐ 配置写面：Editor（原子写 config.yaml + 即时生效）
├── internal/index/             # ⭐ 核心：只读扫描 + 解析 + ID 白名单
│   ├── index.go                # Scan / readArtifact / parseDirName / selectMainFile / Store.Find
│   └── digest.go               # idFor(htmlAbs)[:12]、SHA256File（只读不变性断言用）
├── internal/tags/              # ⭐ 手工多级标签（键 = type/slug，跨交付稳定）
│   └── tags.go                 # Store：YAML 原子写 + 路径校验 + 孤儿 GC
├── internal/thumbs/            # ⭐ 缩略图派生缓存（全仓第二个写面，只写缓存目录）
│   ├── thumbs.go               # Store：命中读盘 / 同 key 单飞 / 全局并发 1 / 原子落盘 / GC
│   └── browser.go              # BrowserCapturer：系统 Chrome/Edge 无头截图（不自写 WebView2 COM）
├── internal/web/               # HTTP 面（产物只读 + 缩略图 + 设置端点）
│   ├── server.go               # mux：GET / 、/api/index(?rescan=1)、/a/{id}、/t/{id}、/api/settings*；//go:embed static
│   ├── thumb.go                # GET /t/{id}：取即建；id 必须先在索引中，缓存键来自索引字段
│   ├── settings.go             # SettingsStore 接口 + 三个设置端点；包内不得出现写文件调用
│   ├── listen.go               # Bind：非回环返回 error（拒绝启动，不降级）
│   └── static/                 # ⭐ 前端：零构建 vanilla JS（index.html / app.js / style.css / favicon.svg）
└── internal/desktop/
    ├── window_windows.go       # WebView2 窗口（NewWithOptions / IconId=2 / Dispatch(Terminate)）
    ├── window_stub.go          # 非 Windows stub
    ├── tray.go / tray_stub.go  # 关闭行为三态与平台无关模型
    ├── tray_windows.go         # 纯 Win32 Shell_NotifyIconW + WM_CLOSE 子类化
    └── open.go
```

分层约束：`internal/index` 不 import `net/http`；`internal/web` 与 `internal/index` 里不得出现任何写
文件系统调用（`os.WriteFile`/`Remove`/`Rename`/`MkdirAll`）。配置写入只允许在
`internal/config.Editor`；缩略图缓存写入只允许在 `internal/thumbs`；标签写入只允许在
`internal/tags`（`web` 通过 `thumbs.Store` / `tags.Store` 调用，自身仍不写盘）：`web` 只依赖 `SettingsStore` 接口，产物目录与用户配置文件严格隔离。
运行期可变的 `Roots` / `RootSource` / `GUI.CloseAction` 由 `config.Config` 的 `RWMutex` 保护，
`index.Scan` 必须走 `RootSnapshot()` / `Source()`，不得裸读字段（设置页与扫描会并发）。

---

## 3. 两条最容易写错的认知陷阱

1. **「只读」不是风格偏好，是功能正确性条件。** archify 交付时把 provenance 写进
   `<stem>.delivery.json`，与 HTML **字节**绑定；viewer 若改写产物（哪怕是「格式化」「补全」），
   用户再跑 `archify check` 会对**自己的产物**判红。因此任何写操作都不许出现，
   `internal/index` 的 `TestScanIsReadOnly` 用 sha256 全量比对钉住这条。
2. **ID 是唯一寻址面，不是便利设计。** `/a/{id}` 之后只查索引期建好的 `id → 绝对路径` map；
   一旦允许 `?path=` 之类参数，路径就来自请求，穿越过滤只能靠正则兜——那是本仓明确拒绝的姿势。
   新增任何取文件能力，必须先入索引再拿 ID。

---

## 4. 数据模型与安全红线

- **产物识别**：`<root>/<dir>` 且该目录**顶层**有 `*.html` 才算产物；只扫一层，`review-<n>`、`visual-check`
  等取证子目录天然被排除（它们在主件的下一层）。目录名按 `^(type)-(slug)-(YYYYMMDD-HHMMSS)$` 解析，类型不在 archify 词表内
  记 `DIRNAME-UNPARSED`。主件优先 `<slug>.html`，其次 `candidate.json` 的 `meta.output`，
  再其次单张/最新一张（多张歧义记 `HTML-AMBIGUOUS`）。次序是硬约定，改动必须有测试钉住。
- **规则码**：`DIRNAME-UNPARSED` / `CANDIDATE-MISSING` / `CANDIDATE-INVALID` / `SIDECAR-MISSING` /
  `SIDECAR-INVALID` / `HTML-AMBIGUOUS` / `ROOT-UNREADABLE` / `ARTIFACT-UNREADABLE: <目录名>`。
  对外只出规则码与目录名，原始错误（内含根绝对路径）只进进程日志。
- **元数据来源全部来自 archify 既有锚点**：`candidate.json` 的 `meta.{title,subtitle,output,
  quality_profile,repository}`、`<stem>.delivery.json` 的 `status`（`current`/`failed`，缺失=`unknown`）。
  本仓不实现任何校验逻辑。
- **绝对路径**只存在于进程内白名单：`Artifact.htmlPath` 与 `RootStatus.Path` 是 `json:"-"`；
  stdout 默认只打 label/RelPath/basename；`list --verbose` 是唯一显式例外。
- **监听**：默认 `127.0.0.1:0`（随机空闲端口，实际地址只能由服务端回报，勿由前端猜测）。
  **非回环地址 ⇒ 拒绝启动**（与 ssh-tunnel-ui 的「静默回落 + 告警」有意不同：本工具无鉴权，
  而产物常带真实仓名、文件路径、commit SHA 与内部架构，等同机密图纸）。显式端口被占 ⇒ 报错退出，
  不静默换端口。
- **缓存**：所有响应 `Cache-Control: no-store`（产物自带 hash 深链，串台缓存会误导人）。
- **缩略图派生缓存**：`GET /t/{id}` 取即建（命中读盘；未命中同 key 单飞、全局并发 1），
  id 必须先在索引中存在；缓存键 `{id}-{mtime}-{size}`，目录 `%AppData%/archify-viewer/thumbs/`，
  随 `Rescan` 清理陈旧项。写盘只发生在 `internal/thumbs`（`internal/web` 自身仍无写调用）。
  采集用系统 Chrome/Edge **无头进程** + 一次性 profile（不自写 WebView2 COM）；
  无浏览器 ⇒ 404 `thumb_unavailable`，前端保持类型占位，不伪装成功。
  **截图专用派生视图**：`GET /s/{id}` 在产物 HTML 的 `</head>` 前注入样式隐藏 `.toolbar` 与 `.header`，
  只截图形部分；`/a/{id}` 仍字节原样。跨 key 并发采集上限 3（`thumbs.concurrency`）。
  **按主题出两张**：缓存键追加 `-light` / `-dark`，`GET /t/{id}?theme=dark` 取深色那张，
  查看器切主题即换一套。深色采集用 `--force-dark-mode` 且**不能同时带 `--virtual-time-budget`**
  （实测会出图但不退出进程），改用 `--timeout=4000` 收口。
- **手工标签**：键 `type/slug`（跨交付稳定，重新交付自动继承）；存 `%AppData%/archify-viewer/tags.yaml`，
  原子写 + 0600 + `tags.yaml.lock` 进程间文件锁（保存 = 拿锁 → 重读磁盘 → 合并本次变更 → 原子回写，
  两实例共用配置目录互不覆盖；锁由 OS 持有，崩溃自动释放）；`gui` 形态带命名 mutex 单实例守卫
  （Windows；守卫自身故障降级放行，第二实例直接退出并提示查托盘）；`POST /api/tags` 幂等整体替换，`/api/index` 每件附 `tags`；`Rescan` 时清理孤儿键。
  路径式层级（`a/b/c`，最多 4 层）；过滤在搜索栏与左侧树（标签/来源/类型多选取**并集**，同键内按前缀匹配子树）。
  **父子语义 = 逻辑包含**：挂 `a/b` 即隐含 `a`，但物理上只存最具体标签——`Normalize` 保存时做段级前缀剪枝
  （`a` 不吞 `ab`），载入旧文件时同样剪枝并回写自愈；树节点展开后列出**该节点路径前缀命中的整棵子树产物**
  （同一产物在节点内去重；子节点展开后父节点仍列整棵子树，不收回）；打标候选树隐藏**已被本卡已挂子级覆盖**
  的父级标签（降为纯分组节点），其余真实存在的标签渲染成可点按钮，纯分组节点不可点。
- 超时：`ReadHeaderTimeout=5s`、`WriteTimeout=60s`、`IdleTimeout=30s`。
- **设置写面**：`GET /api/settings`、`POST /api/settings/roots`、`POST /api/settings/close-action`
  只在 `gui` / `run` 装配，未装配返回 503（能力缺失不伪装成功）。请求体限 1 MiB；
  保存用 `CreateTemp` + `Sync` + `Rename` + `Chmod(0600)`，**先落盘再改运行态**，
  成功后 `srv.Rescan()` 立即重建索引。路径只作为字符串校验「目录存在」，不新增任何按路径取文件的能力。
  「选择目录…」只回填输入框（选择 ≠ 添加），需再点「添加目录」才落盘；行内「移除」按钮的 disabled
  由 `setSettingsBusy` 统一收发——`renderSettings` 可能在 busy=true 时重绘，结束时必须复活，否则永久点不动。
- **Windows 资源与子系统**：`-H windowsgui` 保证双击无控制台黑窗；`console_windows.go` 让 CLI
  调用仍可打印、启动失败无控制台时弹 MessageBox（不静默退出）。`.syso` 资源 ID 是硬约定：
  `RT_MANIFEST=1`、`RT_GROUP_ICON=2`（窗口 `IconId=2` 与托盘 `LoadImageW(2)` 共用）、`RT_ICON=3..6`。
  改 `assets/` 后必须重跑 `go run scripts/gen-icon.go` 并提交生成物，`go build` 才会链进 exe。

---

## 5. 构建与验证命令（机械门）

| 命令 | 说明 | 必须通过 |
|------|-----|---------|
| `go build ./...` | 全量编译 | ✅ |
| `CGO_ENABLED=0 go build ./...` | 依赖面不碰 CGO（原生窗口只在 Windows 且免 CGO） | ✅ |
| `go vet ./...` | 静态检查 | ✅ |
| `go test ./...` | 全量单测（fixture 全用 `t.TempDir()`，无外部依赖） | ✅ |
| `go test -race ./...` | 并发路径（Rescan 换快照 × 请求读、设置页 × `index.Scan`） | ✅ 改 web/index 时；本机无 gcc / `CGO_ENABLED=0` 时须**显式登记未覆盖**，不得用普通 `go test` 冒充 |
| `node --check internal/web/static/app.js` | **前端唯一静态门**（无打包器、无 JS 测试框架） | ✅ 改 static/ 时 |
| `gofmt -l .` | 输出必须为空 | ✅ |
| `go run scripts/gen-icon.go` | 改 `assets/icon.svg` / `windows.manifest` 后重生 ICO 与 `.syso` | ✅ 改图标/清单时 |

出 Windows 产物：`powershell -File scripts/build.ps1` → `build/archify-viewer-windows-amd64.exe`
（`-ldflags "-s -w -H windowsgui"`，`build/` 已 gitignore）。改了 `internal/web/static/` 之后
**必须重新 `go build` 再跑二进制**才能验证界面行为——静态资源经 `//go:embed` 编进二进制，
`node --check` 只证语法、不证渲染。

GUI 子系统与图标的机器可验证口径：解析 PE `OptionalHeader.Subsystem == 2`（`WINDOWS_GUI`）、
`.rsrc` 存在、资源树含 `RT_ICON/RT_GROUP_ICON/RT_MANIFEST`、`RT_GROUP_ICON` 含 ID 2、
图标 PNG 字节确实嵌进 exe。前端交互则用 Node `vm` + 最小 DOM 桩加载真实 `app.js` 做状态流转断言，
再用真实二进制起回环服务做 HTTP 全链路；这三者都**不能替代**托盘/窗口的 GUI 人工走查。

---

## 6. 测试组织

`internal/config` 覆盖三档 roots 优先级、向上发现（只查祖先链不扫盘）、显式 root/config 缺失即失败、
地址校验。`internal/index` 覆盖 10 类型 + explainer 枚举、七条规则码各一例、`meta.output` 主件优先、
同 slug 多版本各自成件且 ID 不撞、ID 重启稳定、mtime 排序、**只读不变性（sha256 全量比对）**、
JSON 无绝对路径、一处根不可读不影响其余、`Find` 的 ID/前缀/子串与歧义不猜。
`internal/web` 覆盖 `/api/index` 形状与 no-store、按 ID 取到**原始字节**、未知/穿越 ID 拒绝、
静态资产白名单（不目录列举、不任意透传）、`?rescan=1` 后新产物立即可取、非回环拒绝启动、
端口占用显式失败、真实 HTTP 请求字节一致。`internal/desktop` 覆盖拉起命令构造、
**目标路径经环境变量传递而不进命令串**（注入面）、`ErrUnsupported` 可被 `errors.Is` 判定。

`internal/config` 另覆盖 `NormalizeCloseAction`、Editor 增删/持久化/保留手写 GUI 字段/原子写、
`RootSnapshot` 副本隔离；`internal/web` 另覆盖设置端点未装配时 503、增删根目录立即重扫、
非法输入不写坏配置、关闭行为运行时生效、favicon 白名单、设置视图不把路径泄漏进 `/api/index`；
`internal/desktop` 另覆盖 `ParseCloseBehavior` 三态与未知值回落。

另有两类钉住真实缺陷的用例：`main_test.go` 的 `TestNewNativeWindow*`（上游 WebView2 初始化 panic
必须收敛成 error 并回退浏览器）、`internal/config/settings_concurrency_test.go` 的并发写入 × 快照读
压力用例（`-race` 门在本机无 gcc 时的**逻辑层替代**，明确不证明无数据竞争）。

**测试隔离**：`TestLoadEmptyRootsIsNotAnError` 用 `t.Setenv("APPDATA", t.TempDir())` 隔离真实用户配置
目录——本机 `%AppData%/archify-viewer/config.yaml` 若存在，`config.Load` 会把它当第二档配置读入，
使「空库」断言拿到用户自己的 roots 而假红。用户配置文件属用户资产，**不删不改**，只在测试进程内隔离。

**没有浏览器级 UI 自动化**——界面改动靠构建产物人工走查留证（主仓 `harness/.../06_test/`）。
自动化部分由「DOM 桩状态探针 + 真实进程 HTTP 全链路 + PE 结构解析 + 真机只读窗口巡检」承担。
真机巡检按 `GetWindowThreadProcessId` 归属过滤（按标题找窗口会误命中别的实例，实测踩过）；
托盘气泡、右键菜单点击、双击恢复等交互仍属人工门，不得用 `docker compose ps healthy`
或 `go test` 绿来冒充已验证。

---

## 7. 关联

- 独立仓：GitLab `wamdy.com/tools/archify-viewer`（组 id 60，private，`main`）。
- appollo 主仓经 `.gitmodules` 以 submodule 引用本仓；在票据工作树内使用需
  `git submodule update --init code/tools/archify-viewer`（否则目录为空壳）。
- 需求与裁决：appollo 主仓票 **#1087**；设计文档 `harness/04-tools/01_function/ISSUE-20261106-archify-viewer/04_design/design.md`。
- 上游生产者：archify 技能包（`tt-a1i/archify`，本机 `~/.qoder-cn/skills/archify`，v3.2）。
  本仓只消费其**目录/sidecar 约定**，不 import、不依赖其运行时。
- 形态先例：`code/tools/ssh-tunnel-ui`（多形态 CLI + 原生窗口回退 + embed 前端）、
  `code/tools/jsonl-table-viewer`（回环监听 + 白名单 + 端口占用即退出）。
- 借用 `ssh-tunnel-ui` 的成熟做法：纯 Win32 托盘（隐藏窗口 + `Shell_NotifyIconW` + 主窗口
  `WM_CLOSE` 子类化）、关闭行为三态、配置原子写与 0600、设置 API/设置页、首帧前写 `data-theme`、
  退出经 `Dispatch(Terminate)` 回 UI 线程、页面内自绘确认不依赖原生弹窗。
  **有意不借**它的非回环监听回落/告警：本工具无鉴权，非回环一律拒绝启动；
  关闭行为默认也从「每次询问」改为 `tray`——本机查看器没有需要用户确认的后台连接。
