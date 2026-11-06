package config

import (
	"os"
	"path/filepath"
	"testing"
)

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestValidateAddrOnlyLoopback(t *testing.T) {
	okCases := []string{"127.0.0.1:0", "127.0.0.1:8899", "localhost:0", "[::1]:0", "127.3.6.2:1234"}
	for _, addr := range okCases {
		if err := ValidateAddr(addr); err != nil {
			t.Errorf("ValidateAddr(%q) 应通过，得到 %v", addr, err)
		}
	}
	// 红线 2：非回环与畸形地址必须是 error（调用方据此拒绝启动，不降级）。
	badCases := []string{"0.0.0.0:8080", ":8080", "*:8080", "192.168.1.10:8080", "example.com:80", "127.0.0.1", "127.0.0.1:notaport", "127.0.0.1:70000"}
	for _, addr := range badCases {
		if err := ValidateAddr(addr); err == nil {
			t.Errorf("ValidateAddr(%q) 应报错，却通过了", addr)
		}
	}
}

func TestIsLoopbackHost(t *testing.T) {
	cases := map[string]bool{
		"127.0.0.1": true, "127.255.255.254": true, "localhost": true, "LOCALHOST": true,
		"::1": true, "0.0.0.0": false, "::": false, "": false, "10.0.0.1": false, "example.com": false,
	}
	for host, want := range cases {
		if got := IsLoopbackHost(host); got != want {
			t.Errorf("IsLoopbackHost(%q)=%v，期望 %v", host, got, want)
		}
	}
}

func TestDiscoverUpFindsArtifactDir(t *testing.T) {
	base := t.TempDir()
	deep := filepath.Join(base, "repo", "code", "tools")
	artifact := filepath.Join(base, ArtifactDirName)
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(artifact, 0o755); err != nil {
		t.Fatal(err)
	}

	root, ok := DiscoverUp(deep)
	if !ok {
		t.Fatalf("自 %s 向上应发现 %s，却没找到", deep, artifact)
	}
	if filepath.Clean(root.Path) != filepath.Clean(artifact) {
		t.Errorf("发现路径 = %s，期望 %s", root.Path, artifact)
	}
	if root.Label != filepath.Base(base) {
		t.Errorf("label = %q，期望父目录名 %q", root.Label, filepath.Base(base))
	}

	// 只查祖先链、不扫盘：另一棵没有 .archify 祖先的树不该命中。
	elsewhere := t.TempDir()
	if missing, ok := DiscoverUp(elsewhere); ok {
		t.Errorf("不应在无关树里命中产物根，得到 %s", missing.Path)
	}
}

func TestLoadPrecedence(t *testing.T) {
	base := t.TempDir()
	flagRoot := filepath.Join(base, "flagged")
	discovered := filepath.Join(base, "repo")
	writeFile(t, filepath.Join(discovered, ArtifactDirName, "placeholder.html"), "<html></html>")
	writeFile(t, filepath.Join(base, "config.yaml"), `roots:
  - path: `+filepath.Join(base, "from-config")+`
    label: 配置来源
gui:
  addr: 127.0.0.1:0
  width: 1200
`)
	writeFile(t, filepath.Join(discovered, ArtifactDirName, "placeholder.html"), "<html></html>")

	t.Run("--root 优先于自动发现与配置", func(t *testing.T) {
		if err := os.MkdirAll(flagRoot, 0o755); err != nil {
			t.Fatal(err)
		}
		cfg, err := Load(Options{ExplicitRoots: []string{flagRoot}, ConfigPath: filepath.Join(base, "config.yaml"), WorkingDir: filepath.Join(discovered, "sub")})
		if err != nil {
			t.Fatal(err)
		}
		if cfg.RootSource != "flag" || len(cfg.Roots) != 1 || filepath.Clean(cfg.Roots[0].Path) != filepath.Clean(flagRoot) {
			t.Errorf("roots 解析不如预期：%+v source=%s", cfg.Roots, cfg.RootSource)
		}
	})

	t.Run("无 --root 时自动发现优先于配置", func(t *testing.T) {
		cfg, err := Load(Options{ConfigPath: filepath.Join(base, "config.yaml"), WorkingDir: filepath.Join(discovered, "sub")})
		if err != nil {
			t.Fatal(err)
		}
		if cfg.RootSource != "discover" {
			t.Errorf("source = %s，期望 discover", cfg.RootSource)
		}
		if len(cfg.Roots) != 1 || !stringsHasSuffix(cfg.Roots[0].Path, ArtifactDirName) {
			t.Errorf("roots = %+v", cfg.Roots)
		}
	})

	t.Run("两者皆无时用配置 roots 并带 label", func(t *testing.T) {
		confRoot := filepath.Join(base, "from-config")
		if err := os.MkdirAll(confRoot, 0o755); err != nil {
			t.Fatal(err)
		}
		// WorkingDir 落在没有任何 .archify 祖先的临时目录里。
		orphan := filepath.Join(base, "orphan")
		if err := os.MkdirAll(orphan, 0o755); err != nil {
			t.Fatal(err)
		}
		cfg, err := Load(Options{ConfigPath: filepath.Join(base, "config.yaml"), WorkingDir: orphan})
		if err != nil {
			t.Fatal(err)
		}
		if cfg.RootSource != "config" {
			t.Fatalf("source = %s，期望 config", cfg.RootSource)
		}
		if len(cfg.Roots) != 1 || cfg.Roots[0].Label != "配置来源" {
			t.Errorf("roots = %+v", cfg.Roots)
		}
		if cfg.GUI.Width != 1200 {
			t.Errorf("width = %d，期望配置文件里的 1200", cfg.GUI.Width)
		}
	})
}

func TestLoadRejectsMissingExplicitRoot(t *testing.T) {
	base := t.TempDir()
	missing := filepath.Join(base, "nope")
	_, err := Load(Options{ExplicitRoots: []string{missing}, WorkingDir: base})
	if err == nil {
		t.Fatal("显式产物根不存在时必须报错（红线 4：不静默跳过）")
	}
}

func TestLoadRejectsMissingConfigFile(t *testing.T) {
	base := t.TempDir()
	if _, err := Load(Options{ConfigPath: filepath.Join(base, "absent.yaml"), WorkingDir: base}); err == nil {
		t.Fatal("显式 --config 指向不存在的文件必须报错")
	}
}

func TestLoadEmptyRootsIsNotAnError(t *testing.T) {
	base := t.TempDir()
	orphan := filepath.Join(base, "orphan")
	if err := os.MkdirAll(orphan, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(Options{WorkingDir: orphan})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.RootSource != "none" || len(cfg.Roots) != 0 {
		t.Errorf("空库应走 none 而不是报错：%+v", cfg)
	}
	if cfg.GUI.Addr != DefaultGUIAddr {
		t.Errorf("默认监听 = %s，期望 %s", cfg.GUI.Addr, DefaultGUIAddr)
	}
}

func TestLoadFlagAddrOverridesAndIsValidated(t *testing.T) {
	base := t.TempDir()
	if _, err := Load(Options{WorkingDir: base, Addr: "0.0.0.0:9999"}); err == nil {
		t.Fatal("--addr 非回环必须拒绝启动（design D-3）")
	}
	cfg, err := Load(Options{WorkingDir: base, Addr: "127.0.0.1:45000"})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.GUI.Addr != "127.0.0.1:45000" {
		t.Errorf("addr = %s", cfg.GUI.Addr)
	}
}

func stringsHasSuffix(path, suffix string) bool {
	return filepath.Base(filepath.Clean(path)) == suffix
}
