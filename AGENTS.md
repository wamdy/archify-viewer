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
**只读**产物库，不渲染、不校验、不改写；无用户体系、无对外监听。
共享服务端（P2）与平台资产化（P3）在主仓 #1087 票面明确划在本仓之外。

---

## 2. 关键目录与分层

```
archify-viewer/
├── main.go                     # CLI 入口（gui/run/list/open/version）
├── console_windows.go          # AttachConsole（-H windowsgui 下 CLI 仍可打印，只用 stdlib syscall）
├── console_other.go
├── go.mod                      # module archify-viewer；依赖仅 go-webview2(+winloader/x/sys 间接) 与 yaml.v3
├── config.example.yaml
├── scripts/build.ps1           # 本机 exe；scripts/build-dist.sh 三平台 + SHA256SUMS
│
├── internal/config/            # ⭐ 三档 roots 发现（--root → 向上找 .archify/ → 配置文件）、addr 回环校验
│   └── config.go:290           # IsLoopbackHost：localhost / 127.0.0.0/8 / ::1；空 host 不算回环
├── internal/index/             # ⭐ 核心：只读扫描 + 解析 + ID 白名单
│   ├── index.go                # Scan / readArtifact / parseDirName / pickHTML / Store.Find
│   └── digest.go               # idFor(htmlAbs)[:12]、SHA256File（只读不变性断言用）
├── internal/web/               # 只读 HTTP 面
│   ├── server.go               # mux：GET / 、GET /api/index(?rescan=1)、GET /a/{id}；//go:embed static
│   ├── listen.go               # Bind：非回环返回 error（拒绝启动，不降级）
│   └── static/                 # ⭐ 前端：零构建 vanilla JS（index.html / app.js / style.css）
└── internal/desktop/           # window_windows.go(WebView2) / window_stub.go(ErrUnsupported) / open.go
```

分层约束：`internal/index` 不 import `net/http`；`internal/web` 与 `internal/index` 里不得出现任何写
文件系统调用（`os.WriteFile`/`Remove`/`Rename`/`MkdirAll`）。

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

- **产物识别**：`<root>/<dir>` 且该目录顶层有 `*.html` 才算产物（`review-*`、`visual-check` 等取证子目录
  在下一层，天然被排除）。目录名按 `^(type)-(slug)-(YYYYMMDD-HHMMSS)$` 解析，类型不在 archify 词表内
  记 `DIRNAME-UNPARSED`。主件优先 `<slug>.html`，其次 `candidate.json` 的 `meta.output`，
  再其次单张/最新一张（多张歧义记 `HTML-AMBIGUOUS`）。
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
- 超时：`ReadHeaderTimeout=5s`、`WriteTimeout=60s`、`IdleTimeout=30s`。

---

## 5. 构建与验证命令（机械门）

| 命令 | 说明 | 必须通过 |
|------|-----|---------|
| `go build ./...` | 全量编译 | ✅ |
| `CGO_ENABLED=0 go build ./...` | 依赖面不碰 CGO（原生窗口只在 Windows 且免 CGO） | ✅ |
| `go vet ./...` | 静态检查 | ✅ |
| `go test ./...` | 全量单测（fixture 全用 `t.TempDir()`，无外部依赖） | ✅ |
| `go test -race ./...` | 并发路径（Rescan 换快照 × 请求读） | ✅ 改 web/index 时 |
| `node --check internal/web/static/app.js` | **前端唯一静态门**（无打包器、无 JS 测试框架） | ✅ 改 static/ 时 |
| `gofmt -l .` | 输出必须为空 | ✅ |

出 Windows 产物：`powershell -File scripts/build.ps1` → `build/archify-view-windows-amd64.exe`
（`-ldflags "-s -w -H windowsgui"`，`build/` 已 gitignore）。改了 `internal/web/static/` 之后
**必须重新 `go build` 再跑二进制**才能验证界面行为——静态资源经 `//go:embed` 编进二进制，
`node --check` 只证语法、不证渲染。

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

**没有浏览器级 UI 自动化**——界面改动靠构建产物人工走查留证（主仓 `harness/.../06_test/`）。

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
