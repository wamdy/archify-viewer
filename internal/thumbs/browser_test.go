package thumbs

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

const helperEnv = "ARCHIFY_THUMBS_HELPER"

// TestMain 兼任「假浏览器」：把测试二进制自身当浏览器拉起，由环境变量切换行为。
// 这样 Capture 的进程编排（参数、stderr 截断、产物读取、临时目录清理）全被真实执行，
// 又不需要机器上真的装浏览器。
func TestMain(m *testing.M) {
	if mode := os.Getenv(helperEnv); mode != "" {
		helperMain(mode)
		return
	}
	os.Exit(m.Run())
}

func helperMain(mode string) {
	if mode == "fail" {
		os.Stderr.WriteString("boom: simulated browser failure\n")
		os.Exit(3)
	}
	for _, a := range os.Args[1:] {
		if strings.HasPrefix(a, "--screenshot=") {
			if err := os.WriteFile(strings.TrimPrefix(a, "--screenshot="), pngBytes(1280, 800), 0o600); err != nil {
				os.Exit(4)
			}
			os.Exit(0)
		}
	}
	os.Exit(5)
}

func TestPickBrowser(t *testing.T) {
	existsAlways := func(string) bool { return true }
	existsNever := func(string) bool { return false }
	noLook := func(string) (string, error) { return "", errors.New("not found") }

	cases := []struct {
		name  string
		env   string
		look  func(string) (string, error)
		ex    func(string) bool
		known []string
		want  string
	}{
		{"显式指定存在", "/opt/chrome", noLook, existsAlways, nil, "/opt/chrome"},
		{"显式指定不存在则不回落", "/nope", noLook, existsNever, []string{"/usr/bin/chrome"}, ""},
		{"PATH 命中", "", func(string) (string, error) { return "/usr/bin/chrome", nil }, existsNever, nil, "/usr/bin/chrome"},
		{"已知路径兜底", "", noLook, func(p string) bool { return p == "/known/edge" }, []string{"/known/edge"}, "/known/edge"},
		{"全都没有", "", noLook, existsNever, []string{"/known/edge"}, ""},
	}
	for _, c := range cases {
		if got := pickBrowser(c.env, c.look, c.ex, c.known); got != c.want {
			t.Errorf("%s: got %q want %q", c.name, got, c.want)
		}
	}
}

func TestCaptureArgsContract(t *testing.T) {
	args := strings.Join(captureArgs("P", "S", "http://127.0.0.1:1/a/x"), " ")
	for _, want := range []string{
		"--headless=new", "--window-size=1280,800", "--virtual-time-budget=2000",
		"--user-data-dir=P", "--screenshot=S", "http://127.0.0.1:1/a/x",
		"--disable-extensions", "--disable-background-networking",
	} {
		if !strings.Contains(args, want) {
			t.Errorf("参数缺 %q：%s", want, args)
		}
	}
}

func TestCaptureWithHelperBrowser(t *testing.T) {
	t.Setenv(helperEnv, "ok")
	c := &BrowserCapturer{bin: os.Args[0], timeout: 10 * time.Second}
	png, err := c.Capture(context.Background(), "http://127.0.0.1:1/a/x")
	if err != nil {
		t.Fatal(err)
	}
	if !isValidPNG(png) {
		t.Error("helper 产出的截图未被识别为 PNG")
	}
}

func TestCaptureHelperFailureCarriesStderr(t *testing.T) {
	t.Setenv(helperEnv, "fail")
	c := &BrowserCapturer{bin: os.Args[0], timeout: 10 * time.Second}
	_, err := c.Capture(context.Background(), "http://127.0.0.1:1/a/x")
	if !errors.Is(err, ErrFailed) {
		t.Fatalf("err = %v, want ErrFailed", err)
	}
	if !strings.Contains(err.Error(), "simulated browser failure") {
		t.Errorf("错误里应带 stderr 摘要：%v", err)
	}
}

func TestCaptureUnavailableWhenNoBrowser(t *testing.T) {
	// 显式指定一个不存在的路径：pickBrowser 按约定不回落 → 判不可用。
	t.Setenv("ARCHIFY_VIEWER_BROWSER", "/definitely/not/a/browser")
	if _, err := NewBrowserCapturer().Capture(context.Background(), "http://127.0.0.1:1/a/x"); !errors.Is(err, ErrUnavailable) {
		t.Errorf("err = %v, want ErrUnavailable", err)
	}
}
