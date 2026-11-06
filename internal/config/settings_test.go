package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestNormalizeCloseAction(t *testing.T) {
	cases := map[string]string{
		"ask": "ask", "tray": "tray", "quit": "quit",
		" ASK ": "ask", "": "tray", "bogus": "tray",
	}
	for in, want := range cases {
		if got := NormalizeCloseAction(in); got != want {
			t.Errorf("NormalizeCloseAction(%q) = %q, want %q", in, got, want)
		}
	}
}

func testConfig() *Config {
	return &Config{
		RootSource: "none",
		GUI:        GUI{Addr: DefaultGUIAddr, Width: 1440, Height: 900, CloseAction: DefaultCloseAction},
	}
}

func readConfigFile(t *testing.T, path string) configFile {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取配置文件失败: %v", err)
	}
	var data configFile
	if err := yaml.Unmarshal(raw, &data); err != nil {
		t.Fatalf("配置文件不是合法 YAML: %v\n%s", err, raw)
	}
	return data
}

func TestEditorAddRemoveAndPersist(t *testing.T) {
	tmp := t.TempDir()
	rootA := filepath.Join(tmp, "library-a")
	rootB := filepath.Join(tmp, "library-b")
	for _, p := range []string{rootA, rootB} {
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	configPath := filepath.Join(tmp, "conf", "config.yaml")
	cfg := testConfig()
	editor, err := NewEditor(cfg, configPath)
	if err != nil {
		t.Fatal(err)
	}

	view := editor.Settings()
	if !view.Editable || len(view.Roots) != 0 || view.CloseAction != "tray" || view.ConfigPath != configPath {
		t.Fatalf("初始快照不符: %+v", view)
	}

	if _, err := editor.AddRoot(""); err == nil {
		t.Error("空路径必须被拒绝")
	}
	if _, err := editor.AddRoot(filepath.Join(tmp, "missing")); err == nil {
		t.Error("不存在的目录必须被拒绝（启动即失败口径）")
	}

	view, err = editor.AddRoot(rootA)
	if err != nil {
		t.Fatalf("添加目录失败: %v", err)
	}
	if len(view.Roots) != 1 || view.Roots[0].Path != rootA {
		t.Fatalf("快照未反映新目录: %+v", view.Roots)
	}
	if cfg.Source() != "config" || len(cfg.RootSnapshot()) != 1 {
		t.Fatalf("运行态未同步: source=%s roots=%+v", cfg.Source(), cfg.RootSnapshot())
	}
	if _, err := os.Stat(configPath); err != nil {
		t.Fatalf("配置文件未落盘: %v", err)
	}

	if _, err := editor.AddRoot(rootA); err == nil {
		t.Error("重复目录必须被拒绝")
	}
	if _, err := editor.AddRoot(rootB); err != nil {
		t.Fatalf("添加第二处目录失败: %v", err)
	}
	if len(cfg.RootSnapshot()) != 2 {
		t.Fatalf("运行态应有 2 处根，实际 %d", len(cfg.RootSnapshot()))
	}

	if _, err := editor.RemoveRoot(rootA); err != nil {
		t.Fatalf("移除目录失败: %v", err)
	}
	if len(cfg.RootSnapshot()) != 1 {
		t.Fatalf("移除后应有 1 处根，实际 %d", len(cfg.RootSnapshot()))
	}
	if _, err := editor.RemoveRoot(rootA); err == nil {
		t.Error("移除不存在的目录应报错")
	}

	if _, err := editor.SetCloseAction("quit"); err != nil {
		t.Fatalf("保存关闭行为失败: %v", err)
	}
	if cfg.CloseActionValue() != "quit" {
		t.Errorf("运行态关闭行为 = %q", cfg.CloseActionValue())
	}
	if _, err := editor.SetCloseAction("nonsense"); err != nil {
		t.Fatal(err)
	}
	if cfg.CloseActionValue() != DefaultCloseAction {
		t.Errorf("未知值应回落到默认 %q，实际 %q", DefaultCloseAction, cfg.CloseActionValue())
	}

	data := readConfigFile(t, configPath)
	if len(data.Roots) != 1 || data.Roots[0].Path != rootB {
		t.Errorf("落盘的 roots 不符: %+v", data.Roots)
	}
	if data.GUI.CloseAction != DefaultCloseAction {
		t.Errorf("落盘的 close_action = %q", data.GUI.CloseAction)
	}

	// 重新加载：持久化往返 + 目录仍可解析。
	cfg2 := testConfig()
	editor2, err := NewEditor(cfg2, configPath)
	if err != nil {
		t.Fatal(err)
	}
	view2 := editor2.Settings()
	if len(view2.Roots) != 1 || view2.Roots[0].Path != rootB {
		t.Errorf("重新加载后目录丢失: %+v", view2.Roots)
	}
}

func TestEditorPreservesExistingGUIAndAtomicWrite(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "config.yaml")
	original := "roots:\n  - path: \"" + filepath.ToSlash(tmp) + "\"\n    label: keep\ngui:\n  addr: \"127.0.0.1:0\"\n  width: 1234\n  height: 678\n  close_action: ask\n"
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := testConfig()
	cfg.GUI.Width, cfg.GUI.Height = 1234, 678
	editor, err := NewEditor(cfg, path)
	if err != nil {
		t.Fatal(err)
	}
	extra := filepath.Join(tmp, "extra")
	if err := os.MkdirAll(extra, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := editor.AddRoot(extra); err != nil {
		t.Fatal(err)
	}
	data := readConfigFile(t, path)
	if data.GUI.Addr != "127.0.0.1:0" || data.GUI.Width != 1234 || data.GUI.Height != 678 {
		t.Errorf("保存时丢失了用户原有 GUI 配置: %+v", data.GUI)
	}
	if len(data.Roots) != 2 || data.Roots[0].Label != "keep" {
		t.Errorf("原有 roots 被覆盖: %+v", data.Roots)
	}
	// 原子写不应留下临时文件。
	entries, err := os.ReadDir(tmp)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.Contains(e.Name(), ".tmp") {
			t.Errorf("残留临时文件: %s", e.Name())
		}
	}
}

func TestRootSnapshotIsolated(t *testing.T) {
	cfg := testConfig()
	cfg.SetRoots([]Root{{Label: "a", Path: "a"}}, "config")
	snap := cfg.RootSnapshot()
	snap[0].Path = "tampered"
	if cfg.RootSnapshot()[0].Path != "a" {
		t.Error("RootSnapshot 必须返回副本，调用方不得改到运行态")
	}
}
