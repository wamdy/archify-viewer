package main

import (
	"testing"

	"archify-viewer/internal/config"
)

// 钉住曾真实发生过的缺陷：listFlags 按值复制 launchFlags 后，-root 注册在副本之外，
// 命令行喂了产物根却索引到空库（形状对、用法错的典型）。
func TestListFlagsRootReachesConfig(t *testing.T) {
	root := t.TempDir()
	fs, base := newFlagSet("list")
	lf := &listFlags{launchFlags: base}
	fs.StringVar(&lf.typeVal, "type", "", "")
	fs.IntVar(&lf.limit, "limit", 0, "")
	if err := fs.Parse([]string{"-root", root, "-type", "erd", "-limit", "3"}); err != nil {
		t.Fatal(err)
	}
	if got := lf.options().ExplicitRoots; len(got) != 1 || got[0] != root {
		t.Fatalf("-root 没传到 config.Options： %#v", got)
	}
	if base.root.values[0] != root || lf.root.values[0] != root {
		t.Errorf("lf 与 base 不是同一份 flag 载体：%v vs %v", lf.root.values, base.root.values)
	}
	cfg, err := config.Load(lf.options())
	if err != nil {
		t.Fatal(err)
	}
	if cfg.RootSource != "flag" || len(cfg.Roots) != 1 {
		t.Errorf("roots 解析：%+v source=%s", cfg.Roots, cfg.RootSource)
	}
	if filepathBase(cfg.Roots[0].Path) != filepathBase(root) {
		t.Errorf("root 路径 = %s，期望 %s", cfg.Roots[0].Path, root)
	}
}

func TestRepeatableRootFlag(t *testing.T) {
	a, b := t.TempDir(), t.TempDir()
	fs, base := newFlagSet("gui")
	if err := fs.Parse([]string{"-root", a, "-root", b}); err != nil {
		t.Fatal(err)
	}
	if len(base.root.values) != 2 {
		t.Fatalf("-root 必须可重复，得到 %v", base.root.values)
	}
	cfg, err := config.Load(base.options())
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Roots) != 2 || cfg.Roots[0].Label == cfg.Roots[1].Label {
		t.Errorf("多根应各自带 label：%+v", cfg.Roots)
	}
}

func TestSplitTypesAndContainsFold(t *testing.T) {
	types := splitTypes(" erd , tree,,workflow ")
	if len(types) != 3 || types[0] != "erd" {
		t.Fatalf("splitTypes = %v", types)
	}
	if splitTypes("") != nil {
		t.Error("空串应返回 nil（表示不过滤）")
	}
	if !containsFold(types, "TREE") {
		t.Error("类型匹配应大小写不敏感")
	}
}

func TestHumanBytes(t *testing.T) {
	cases := map[int64]string{512: "512B", 2048: "2.0KB", 5 * 1024 * 1024: "5.0MB"}
	for in, want := range cases {
		if got := humanBytes(in); got != want {
			t.Errorf("humanBytes(%d) = %s，期望 %s", in, got, want)
		}
	}
}

func filepathBase(p string) string {
	for i := len(p) - 1; i >= 0; i-- {
		if p[i] == '\\' || p[i] == '/' {
			return p[i+1:]
		}
	}
	return p
}
