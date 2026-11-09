package thumbs

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const (
	browserTimeout = 20 * time.Second
	maxStderr      = 4096
	viewportW      = 1280
	viewportH      = 800
	virtualBudget  = 2000 // 毫秒；实测 4000 会让等满的请求多花 ~5s
)

// BrowserCapturer 用系统已装的 Chrome / Edge / Chromium 无头模式截图。
//
// 为什么不用 WebView2：jchv/go-webview2 的公开方法面不含 CapturePreview
// （只有底层 pkg/edge 的 COM 虚表有），自写 COM + 离屏窗口 + IStream 的代价远高于
// 起一个一次性浏览器进程；进程方案还能让 run 形态（无原生窗口）同样可用。
type BrowserCapturer struct {
	bin     string        // 显式指定；测试或排障用
	timeout time.Duration // 单次采集上限
}

func NewBrowserCapturer() *BrowserCapturer {
	return &BrowserCapturer{timeout: browserTimeout}
}

// Capture 采集 url 的首屏视口截图，返回 PNG 字节。临时 profile 与截图文件用后即删。
func (c *BrowserCapturer) Capture(ctx context.Context, url string) ([]byte, error) {
	bin := ""
	timeout := browserTimeout
	if c != nil {
		bin = c.bin
		if c.timeout > 0 {
			timeout = c.timeout
		}
	}
	if bin == "" {
		bin = resolveBrowser()
	}
	if bin == "" {
		return nil, ErrUnavailable
	}

	tmp, err := os.MkdirTemp("", "archify-thumb-")
	if err != nil {
		return nil, fmt.Errorf("%w: 建临时目录: %v", ErrFailed, err)
	}
	defer os.RemoveAll(tmp)

	shot := filepath.Join(tmp, "shot.png")
	args := captureArgs(filepath.Join(tmp, "profile"), shot, url)

	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(cctx, bin, args...)
	cmd.Stdout = io.Discard // 浏览器日志不得污染 CLI stdout（list --json 之类靠 stdout 消费）
	var stderr limitedBuffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		if errors.Is(cctx.Err(), context.DeadlineExceeded) {
			return nil, fmt.Errorf("%w: 采集超时（%s）", ErrFailed, timeout)
		}
		if cctx.Err() != nil {
			return nil, fmt.Errorf("%w: %v", ErrFailed, cctx.Err())
		}
		return nil, fmt.Errorf("%w: 浏览器退出异常: %v — %s", ErrFailed, err, stderr.tail(300))
	}

	png, err := os.ReadFile(shot)
	if err != nil {
		return nil, fmt.Errorf("%w: 未产出截图: %v", ErrFailed, err)
	}
	if !isValidPNG(png) {
		return nil, fmt.Errorf("%w: 截图不是有效 PNG", ErrFailed)
	}
	return png, nil
}

// captureArgs 固定采集参数：顶部 1280×800 视口、独立临时 profile、禁用后台网络与扩展。
func captureArgs(profile, shot, url string) []string {
	return []string{
		"--headless=new",
		"--disable-gpu",
		"--hide-scrollbars",
		"--no-first-run",
		"--no-default-browser-check",
		"--disable-extensions",
		"--disable-background-networking",
		"--disable-sync",
		"--mute-audio",
		"--user-data-dir=" + profile,
		"--screenshot=" + shot,
		fmt.Sprintf("--window-size=%d,%d", viewportW, viewportH),
		fmt.Sprintf("--virtual-time-budget=%d", virtualBudget),
		url,
	}
}

// resolveBrowser 决定用哪个浏览器：环境变量 → PATH → 各平台常见安装路径。
func resolveBrowser() string {
	return pickBrowser(os.Getenv("ARCHIFY_VIEWER_BROWSER"), exec.LookPath, fileExists, knownBrowserPaths(runtime.GOOS))
}

// pickBrowser 是可测的纯决策：显式指定但不存在时**不回落**，直接判不可用
// （静默换成别的浏览器会让人误判覆盖生效）。
func pickBrowser(env string, lookPath func(string) (string, error), exists func(string) bool, known []string) string {
	if env != "" {
		if exists(env) {
			return env
		}
		if p, err := lookPath(env); err == nil && p != "" {
			return p
		}
		return ""
	}
	for _, name := range []string{
		"chrome", "chrome.exe", "msedge", "msedge.exe",
		"chromium", "chromium.exe", "chromium-browser", "google-chrome", "microsoft-edge",
	} {
		if p, err := lookPath(name); err == nil && p != "" {
			return p
		}
	}
	for _, p := range known {
		if p != "" && exists(p) {
			return p
		}
	}
	return ""
}

func knownBrowserPaths(goos string) []string {
	switch goos {
	case "windows":
		var out []string
		for _, base := range []string{
			os.Getenv("ProgramFiles"),
			os.Getenv("ProgramFiles(x86)"),
			os.Getenv("LOCALAPPDATA"),
		} {
			if base == "" {
				continue
			}
			out = append(out,
				filepath.Join(base, `Google\Chrome\Application\chrome.exe`),
				filepath.Join(base, `Microsoft\Edge\Application\msedge.exe`),
				filepath.Join(base, `Chromium\Application\chrome.exe`),
			)
		}
		return out
	case "darwin":
		return []string{
			"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
			"/Applications/Microsoft Edge.app/Contents/MacOS/Microsoft Edge",
			"/Applications/Chromium.app/Contents/MacOS/Chromium",
		}
	default:
		return []string{
			"/usr/bin/google-chrome",
			"/usr/bin/chromium",
			"/usr/bin/chromium-browser",
			"/usr/bin/microsoft-edge",
			"/snap/bin/chromium",
		}
	}
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

// limitedBuffer 只保留前 max 字节的 stderr，避免异常浏览器刷爆日志。
type limitedBuffer struct {
	buf bytes.Buffer
	max int
}

func (l *limitedBuffer) Write(p []byte) (int, error) {
	n := len(p)
	if l.max == 0 {
		l.max = maxStderr
	}
	if room := l.max - l.buf.Len(); room > 0 {
		if len(p) > room {
			p = p[:room]
		}
		l.buf.Write(p)
	}
	return n, nil // 永远报「全额写入」，避免子进程因短写提前退出
}

func (l *limitedBuffer) tail(n int) string {
	s := strings.TrimSpace(l.buf.String())
	if len(s) > n {
		s = s[len(s)-n:]
	}
	return s
}
