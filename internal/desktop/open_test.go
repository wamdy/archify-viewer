package desktop

import (
	"errors"
	"runtime"
	"strings"
	"testing"
)

type call struct {
	name string
	args []string
	env  []string
}

// stubRunner 替换 run，返回依次排队好的错误（nil 表示成功）。
func stubRunner(t *testing.T, results ...error) *[]call {
	t.Helper()
	var recorded []call
	queue := append([]error{}, results...)
	original := run
	run = func(c command) error {
		recorded = append(recorded, call{name: c.Name, args: append([]string{}, c.Args...), env: append([]string{}, c.Env...)})
		if len(queue) == 0 {
			return nil
		}
		err := queue[0]
		queue = queue[1:]
		return err
	}
	t.Cleanup(func() { run = original })
	return &recorded
}

func TestOpenURLUsesDefaultHandler(t *testing.T) {
	calls := stubRunner(t)
	if err := OpenURL("http://127.0.0.1:45000/"); err != nil {
		t.Fatal(err)
	}
	if len(*calls) == 0 {
		t.Fatal("没有拉起任何进程")
	}
	last := (*calls)[len(*calls)-1]
	if runtime.GOOS == "windows" {
		if last.name != "rundll32.exe" {
			t.Errorf("Windows 首选 rundll32，得到 %s", last.name)
		}
		if last.args[len(last.args)-1] != "http://127.0.0.1:45000/" {
			t.Errorf("URL 必须整体作为单个参数传递（不进命令串）：%v", last.args)
		}
	}
}

// 红线相关：用户文件路径绝不允许被拼进 shell/PowerShell 命令串。
func TestWindowsFallbackPassesTargetViaEnv(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("仅 Windows 有 rundll32 → PowerShell 回落链")
	}
	tricky := `C:\Temp\a b'";&calc$.html`
	calls := stubRunner(t, errors.New("rundll32 不可用"))
	if err := OpenFile(tricky); err != nil {
		t.Fatalf("回落路径应成功，得到 %v", err)
	}
	if len(*calls) != 2 {
		t.Fatalf("期望两次拉起（失败后回落），得到 %d", len(*calls))
	}
	fallback := (*calls)[1]
	if fallback.name != "powershell.exe" {
		t.Errorf("回落到 powershell，得到 %s", fallback.name)
	}
	joined := strings.Join(fallback.args, " ")
	if strings.Contains(joined, tricky) || strings.Contains(joined, "calc") {
		t.Errorf("目标路径泄进了命令串（注入面）：%s", joined)
	}
	var found string
	for _, kv := range fallback.env {
		if strings.HasPrefix(kv, "ARCHIFY_VIEWER_OPEN_TARGET=") {
			found = strings.TrimPrefix(kv, "ARCHIFY_VIEWER_OPEN_TARGET=")
		}
	}
	if found != tricky {
		t.Errorf("目标应经环境变量传递，得到 %q", found)
	}
}

func TestOpenFileFailureIsReported(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("回落链是 Windows 专属")
	}
	stubRunner(t, errors.New("首选失败"), errors.New("回落也失败"))
	err := OpenFile(`C:\nope\x.html`)
	if err == nil {
		t.Fatal("两次拉起都失败时必须返回错误")
	}
	if !strings.Contains(err.Error(), "拉起系统默认处理程序失败") {
		t.Errorf("错误信息不符：%v", err)
	}
}

func TestErrUnsupportedIsWrapped(t *testing.T) {
	err := Available()
	if runtime.GOOS == "windows" {
		// Windows 上取决于是否装了 WebView2；无论结果如何都不能 panic，
		// 且不可用时必须是 ErrUnsupported，调用方的 errors.Is 判定才成立。
		if err != nil && !errors.Is(err, ErrUnsupported) {
			t.Errorf("Windows 上 Available 报错必须是 ErrUnsupported：%v", err)
		}
		return
	}
	if err == nil {
		t.Fatal("非 Windows 平台必须报不支持，走浏览器回退")
	}
	if !errors.Is(err, ErrUnsupported) {
		t.Errorf("必须是 ErrUnsupported（errors.Is 可判定），得到 %v", err)
	}
	if _, err := New("t", "http://127.0.0.1:1/", 800, 600); !errors.Is(err, ErrUnsupported) {
		t.Errorf("New 也必须回 ErrUnsupported，得到 %v", err)
	}
}
