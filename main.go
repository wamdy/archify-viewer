// Command archify-view 是 archify 产物库的单机查看器（appollo 主仓票 #1087，P1）。
//
// 一个入口四种形态：
//
//	gui   原生 WebView2 窗口（缺运行时/非 Windows 自动回退系统浏览器）
//	run   前台只起本地 HTTP 服务，打印 URL
//	list  终端清单，不起服务、不开窗口，供脚本与 AI 消费
//	open  把指定产物交给系统默认处理程序（file:// 直开，无服务）
//
// 三条红线（详见 AGENTS.md §4）：只读产物、仅回环监听（非回环拒绝启动）、只认 ID 寻址。
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"

	"archify-viewer/internal/config"
	"archify-viewer/internal/desktop"
	"archify-viewer/internal/index"
	"archify-viewer/internal/web"
)

const buildVersion = "1087"

func main() {
	log.SetFlags(log.LstdFlags)

	args := os.Args[1:]
	if len(args) == 0 {
		// 双击启动不带参数时默认进入图形界面。
		args = []string{"gui"}
	} else {
		ensureConsole()
	}

	switch args[0] {
	case "gui":
		guiCmd(args[1:])
	case "run":
		runCmd(args[1:])
	case "list":
		listCmd(args[1:])
	case "open":
		openCmd(args[1:])
	case "version", "--version":
		fmt.Printf("archify-view %s\n", buildVersion)
	case "help", "-h", "--help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "未知命令: %s\n\n", args[0])
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `archify-view - archify 产物库单机查看器

用法:
  archify-view                                  等价于 gui
  archify-view gui   [flags] [--no-browser]     原生窗口（不可用时回退浏览器）
  archify-view run   [flags]                    前台起服务并打印 URL，Ctrl-C 退出
  archify-view list  [flags] [--json] [--type T] [--limit N] [--verbose]
  archify-view open  [flags] <id|slug|产物.html>
  archify-view version

flags:
`)
	flag.CommandLine.VisitAll(func(*flag.Flag) {})
	fmt.Fprint(os.Stderr, "  -root PATH        产物根（可重复）；优先级高于自动发现与配置文件\n"+
		"  -config PATH      配置文件（默认找 ./config.yaml，其次用户配置目录 archify-viewer/config.yaml）\n"+
		"  -addr H:PORT      监听地址，只允许回环（默认 127.0.0.1:0）；非回环直接拒绝启动\n"+
		"  -width / -height  原生窗口尺寸\n"+
		"  -no-browser       gui 形态强制走系统浏览器\n"+
		"  -no-open          只起服务，不开窗口也不拉浏览器\n\n"+
		"产物根发现顺序：-root → 当前目录逐级向上找 .archify/ → 配置文件 roots。\n"+
		"本工具只读：不渲染、不校验、不改写任何 archify 产物。\n")
}

type launchFlags struct {
	root       stringsValue
	configPath string
	addr       string
	width      int
	height     int
	noBrowser  bool
	noOpen     bool
}

// stringsValue 让 -root 可重复传入。
type stringsValue struct{ values []string }

func (s *stringsValue) String() string { return strings.Join(s.values, ",") }
func (s *stringsValue) Set(v string) error {
	if v == "" {
		return errors.New("产物根路径不能为空")
	}
	s.values = append(s.values, v)
	return nil
}

func newFlagSet(name string) (*flag.FlagSet, *launchFlags) {
	fs := flag.NewFlagSet(name, flag.ExitOnError)
	lf := &launchFlags{}
	fs.Var(&lf.root, "root", "产物根（可重复）")
	fs.StringVar(&lf.configPath, "config", "", "配置文件路径")
	fs.StringVar(&lf.addr, "addr", "", "监听地址（仅允许回环）")
	fs.IntVar(&lf.width, "width", 0, "原生窗口宽度")
	fs.IntVar(&lf.height, "height", 0, "原生窗口高度")
	fs.BoolVar(&lf.noBrowser, "no-browser", false, "强制使用系统浏览器")
	fs.BoolVar(&lf.noOpen, "no-open", false, "不开窗口、不拉浏览器")
	return fs, lf
}

func (lf *launchFlags) options() config.Options {
	return config.Options{
		ExplicitRoots: lf.root.values,
		ConfigPath:    lf.configPath,
		Addr:          lf.addr,
		Width:         lf.width,
		Height:        lf.height,
	}
}

// prepare 加载配置、扫描产物库。cfg.RootSource=="none" 不是错误：空库交给 UI 引导。
func prepare(lf *launchFlags) (*config.Config, *index.Index, *index.Store) {
	cfg, err := config.Load(lf.options())
	if err != nil {
		reportFatal(err)
	}
	idx, store := index.Scan(cfg)
	return cfg, idx, store
}

func logIndex(idx *index.Index) {
	parts := make([]string, 0, len(idx.Roots))
	for _, r := range idx.Roots {
		parts = append(parts, fmt.Sprintf("%s(%d)", r.Label, r.Count))
	}
	scope := "无产物根"
	if len(parts) > 0 {
		scope = "来源 " + idx.RootSource + ": " + strings.Join(parts, " ")
	}
	log.Printf("已索引 %d 件产物，%s", len(idx.Artifacts), scope)
	if idx.ParseWarnings > 0 {
		log.Printf("其中 %d 项元数据未能完全解析（不影响打开）", idx.ParseWarnings)
	}
}

// newSettingsEditor 装配设置编辑器；失败只降级「自定义目录」能力，不阻断查看器启动。
func newSettingsEditor(cfg *config.Config, explicit string) *config.Editor {
	editor, err := config.NewEditor(cfg, explicit)
	if err != nil {
		log.Printf("设置编辑器不可用（自定义目录功能将不可用）: %v", err)
		return nil
	}
	return editor
}

// settingsOption 把编辑器注入 HTTP 面；nil 时返回 nil Option，web.New 会忽略。
func settingsOption(editor *config.Editor) web.Option {
	if editor == nil {
		return nil
	}
	return web.WithSettings(editor)
}

// newNativeWindow 把上游 go-webview2 的 panic 收敛成可判定的 error。
// 该库 CreateCoreWebView2ControllerCompleted 对 nil controller 未判空（chromium.go:190），
// 在 WebView2 运行时异常或同一 exe 已有实例占用用户数据目录时会 panic；
// 本工具是 windowsgui 子系统产物，没有控制台，panic 表现为「双击后窗口静默消失」，
// 比可控地回退系统浏览器更糟。recover 后由调用方走既有浏览器回退路径。
var nativeWindowFactory = desktop.New

func newNativeWindow(title, url string, width, height int) (win *desktop.Window, err error) {
	defer func() {
		if r := recover(); r != nil {
			win = nil
			err = fmt.Errorf("原生窗口初始化失败: %v", r)
		}
	}()
	return nativeWindowFactory(title, url, width, height)
}

func guiCmd(args []string) {
	fs, lf := newFlagSet("gui")
	_ = fs.Parse(args)

	cfg, idx, store := prepare(lf)
	logIndex(idx)

	editor := newSettingsEditor(cfg, lf.configPath)
	srv, err := web.New(cfg, idx, store, settingsOption(editor))
	if err != nil {
		reportFatal(err)
	}
	if err := srv.Start(); err != nil {
		reportFatal(err)
	}
	url := srv.URL()
	log.Printf("服务已启动: %s", url)

	var once sync.Once
	shutdown := func() {
		once.Do(func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := srv.Shutdown(ctx); err != nil {
				log.Printf("关闭服务失败: %v", err)
			}
		})
	}

	if lf.noOpen {
		log.Printf("--no-open：仅驻留服务，按 Ctrl-C 退出")
		waitInterrupt()
		shutdown()
		return
	}

	if !lf.noBrowser && desktop.Available() == nil {
		// WebView2 要求窗口在主操作系统线程上创建与运行。
		runtime.LockOSThread()
		win, werr := newNativeWindow("Archify 产物库", url, cfg.GUI.Width, cfg.GUI.Height)
		if werr == nil {
			// 关闭行为每次 WM_CLOSE 都重新读配置，设置页修改后无需重启。
			// 托盘「退出」必须经 win.Close() → Dispatch(Terminate) 回到 UI 线程，不能直接 Quit。
			if terr := win.EnableTray(desktop.TrayOptions{
				Behavior: func() desktop.CloseBehavior {
					return desktop.ParseCloseBehavior(cfg.CloseActionValue())
				},
				OnQuit: func() { win.Close() },
				OnRescan: func() {
					if err := srv.Rescan(); err != nil {
						log.Printf("托盘触发重新扫描失败: %v", err)
					} else {
						log.Printf("托盘触发重新扫描完成")
					}
				},
			}); terr != nil {
				log.Printf("系统托盘不可用，关闭窗口将直接退出: %v", terr)
			}
			win.Run()
			desktop.RemoveTray()
			shutdown()
			return
		}
		log.Printf("原生窗口不可用，回退系统浏览器: %v", werr)
	}

	if err := desktop.OpenURL(url); err != nil {
		log.Printf("拉起浏览器失败，请手工打开 %s（%v）", url, err)
	} else {
		log.Printf("已在系统浏览器打开 %s；关闭页签不会停止服务，按 Ctrl-C 退出", url)
	}
	waitInterrupt()
	shutdown()
}

func runCmd(args []string) {
	fs, lf := newFlagSet("run")
	_ = fs.Parse(args)

	cfg, idx, store := prepare(lf)
	logIndex(idx)

	srv, err := web.New(cfg, idx, store, settingsOption(newSettingsEditor(cfg, lf.configPath)))
	if err != nil {
		reportFatal(err)
	}
	if err := srv.Start(); err != nil {
		reportFatal(err)
	}
	log.Printf("服务已启动: %s", srv.URL())
	waitInterrupt()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		log.Printf("关闭服务失败: %v", err)
	}
}

// listFlags 内嵌**指针**：newFlagSet 把 -root 注册在 base 的 stringsValue 地址上，
// 按值复制会让 lf.root 变成空副本，-root 静默失效（实测踩过）。
type listFlags struct {
	*launchFlags
	jsonOut bool
	verbose bool
	typeVal string
	limit   int
}

func listCmd(args []string) {
	fs, base := newFlagSet("list")
	lf := &listFlags{launchFlags: base}
	fs.BoolVar(&lf.jsonOut, "json", false, "输出 /api/index 同构 JSON")
	fs.BoolVar(&lf.verbose, "verbose", false, "额外打印绝对路径（仅此显式选项会暴露绝对路径）")
	fs.StringVar(&lf.typeVal, "type", "", "只看某个类型（可重复，逗号分隔）")
	fs.IntVar(&lf.limit, "limit", 0, "最多列出多少件（0 = 全部）")
	_ = fs.Parse(args)

	_, idx, _ := prepare(lf.launchFlags)

	list := idx.Artifacts
	if types := splitTypes(lf.typeVal); len(types) > 0 {
		var filtered []*index.Artifact
		for _, art := range list {
			if containsFold(types, art.Type) {
				filtered = append(filtered, art)
			}
		}
		list = filtered
	}
	if lf.limit > 0 && len(list) > lf.limit {
		list = list[:lf.limit]
	}

	if lf.jsonOut {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(idx); err != nil {
			reportFatal(err)
		}
		return
	}

	if len(list) == 0 {
		fmt.Println("没有找到产物。archify 把产物写到当时工作目录下的 .archify/<type>-<slug>-<时间戳>/；")
		fmt.Println("可用 -root 显式指定，或在 config.yaml 里登记 roots。详见 archify-view help。")
		return
	}
	fmt.Printf("%-12s %-12s %-8s %-7s %-16s %s\n", "ID", "TYPE", "DELIVERY", "SIZE", "MODIFIED", "TITLE")
	for _, art := range list {
		fmt.Printf("%-12s %-12s %-8s %-7s %-16s %s\n",
			art.ID, art.Type, art.Delivery, humanBytes(art.Size),
			time.Unix(art.ModTime, 0).Format("2006-01-02 15:04"), art.Title)
		fmt.Printf("%-12s %-12s %-8s %-7s %-16s   ↳ %s/%s\n", "", "", "", "", "", art.Root, art.RelPath)
		if lf.verbose {
			fmt.Printf("%-12s   绝对路径: %s\n", "", art.HTMLPath())
		}
	}
}

func openCmd(args []string) {
	fs, base := newFlagSet("open")
	_ = fs.Parse(args)

	rest := fs.Args()
	if len(rest) != 1 {
		fmt.Fprintln(os.Stderr, "用法: archify-view open [-root PATH] <id|slug|产物.html>")
		os.Exit(2)
	}
	target := rest[0]

	// 显式文件路径：用户直接喂产物，无需索引；仍只做 stat 校验，不服务也不改写。
	if abs, err := filepath.Abs(target); err == nil && strings.EqualFold(filepath.Ext(abs), ".html") && fileExists(abs) {
		if err := desktop.OpenFile(abs); err != nil {
			reportFatal(err)
		}
		// 回显只用 basename：绝对路径是用户自己敲进来的，复读一遍不增加信息，
		// 却会破坏「仅 --verbose 输出绝对路径」这条口径（评审 QUESTION-2）。
		fmt.Printf("已交给系统默认处理程序: %s\n", filepath.Base(abs))
		return
	}

	_, _, store := prepare(base)
	art, err := store.Find(target)
	if err != nil {
		var amb *index.ErrAmbiguous
		if errors.As(err, &amb) {
			fmt.Fprintf(os.Stderr, "%v\n", err)
			os.Exit(1)
		}
		fmt.Fprintf(os.Stderr, "打开失败: %v（可先跑 archify-view list 取 ID）\n", err)
		os.Exit(1)
	}
	if err := desktop.OpenFile(art.HTMLPath()); err != nil {
		reportFatal(err)
	}
	// 默认只打印相对口径，绝对路径留给 --verbose 场景。
	fmt.Printf("已打开 %s/%s（ID %s）\n", art.Root, art.RelPath, art.ID)
}

func waitInterrupt() {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, os.Interrupt, syscall.SIGTERM)
	<-ch
}

func splitTypes(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func containsFold(list []string, want string) bool {
	for _, v := range list {
		if strings.EqualFold(v, want) {
			return true
		}
	}
	return false
}

func humanBytes(n int64) string {
	switch {
	case n < 1024:
		return fmt.Sprintf("%dB", n)
	case n < 1024*1024:
		return fmt.Sprintf("%.1fKB", float64(n)/1024)
	default:
		return fmt.Sprintf("%.1fMB", float64(n)/1024/1024)
	}
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}
