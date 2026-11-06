package desktop

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
)

// command 是一次外部拉起请求。把 env 单列出来，是为了让 Windows 回落路径
// 不把用户文件路径插进 PowerShell 命令串（先例：archify 技能包 bin/open-artifact.mjs）。
type command struct {
	Name string
	Args []string
	Env  []string
}

// run 留出注入点供单测替换，真实实现只用 stdlib os/exec。
var run = func(c command) error {
	cmd := exec.Command(c.Name, c.Args...)
	if len(c.Env) > 0 {
		cmd.Env = c.Env
	}
	return cmd.Run()
}

// OpenURL 把回环服务地址交给系统默认浏览器。失败由调用方降级为日志，不影响服务存活。
func OpenURL(url string) error { return openTarget(url) }

// OpenFile 把产物 HTML 交给系统文件关联（默认浏览器）。产物自包含，file:// 直开可用。
func OpenFile(path string) error { return openTarget(path) }

func openTarget(target string) error {
	switch runtime.GOOS {
	case "windows":
		if err := run(command{Name: "rundll32.exe", Args: []string{"url.dll,FileProtocolHandler", target}}); err == nil {
			return nil
		}
		if err := run(startFallbackWindows(target)); err != nil {
			return fmt.Errorf("拉起系统默认处理程序失败（%s）: %w", target, err)
		}
		return nil
	case "darwin":
		return run(command{Name: "open", Args: []string{target}})
	default:
		return run(command{Name: "xdg-open", Args: []string{target}})
	}
}

func startFallbackWindows(target string) command {
	env := append(os.Environ(), "ARCHIFY_VIEWER_OPEN_TARGET="+target)
	return command{
		Name: "powershell.exe",
		Args: []string{"-NoProfile", "-NonInteractive", "-Command",
			"Start-Process -FilePath $env:ARCHIFY_VIEWER_OPEN_TARGET"},
		Env: env,
	}
}
