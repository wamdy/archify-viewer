// Package config 负责 archify-viewer 的三档产物根发现、配置文件加载与监听地址校验。
//
// 红线（对应 design.md §五-1 / tasks.md 四-3）：
//   - gui/run 的 HTTP 监听只允许回环地址；非回环一律返回 error，由调用方终止进程，
//     **有意不同于** ssh-tunnel-ui 的「静默回落 127.0.0.1 + 告警」——共享监听属 P2，未实现时
//     启动失败比降级更不可能被忽略。
//   - 显式给出的产物根路径不存在即失败，不静默跳过。
package config

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// ArtifactDirName 是 archify 技能包约定的产物目录名（技能包 SKILL.md:14）。
const ArtifactDirName = ".archify"

// DefaultGUIAddr 端口 0 = 系统分配空闲端口，因此重复启动互不冲突。
const DefaultGUIAddr = "127.0.0.1:0"

const (
	defaultWidth  = 1440
	defaultHeight = 900
)

// Root 是一处产物根。Label 用于分组展示（哪个仓/哪台机器），也是未来 P2 的溯源字段。
type Root struct {
	Label string
	Path  string
}

// GUI 是桌面/浏览器形态的监听与窗口参数。
type GUI struct {
	Addr   string
	Width  int
	Height int
}

// Config 是校验后的最终配置。
type Config struct {
	Roots      []Root
	GUI        GUI
	ConfigPath string // 实际加载到的配置文件，未加载则为空
	RootSource string // "flag" | "discover" | "config" | "none"
}

// Options 汇总命令行入参（空值表示未提供，交由配置文件/默认值补足）。
type Options struct {
	ExplicitRoots []string // --root，可重复
	ConfigPath    string   // --config
	Addr          string   // --addr
	Width         int
	Height        int
	WorkingDir    string // 空则取 os.Getwd()，测试注入用
}

type fileRoot struct {
	Path  string `yaml:"path"`
	Label string `yaml:"label"`
}

type fileGUI struct {
	Addr   string `yaml:"addr"`
	Width  int    `yaml:"width"`
	Height int    `yaml:"height"`
}

type configFile struct {
	Roots []fileRoot `yaml:"roots"`
	GUI   fileGUI    `yaml:"gui"`
}

// Load 按优先级解析产物根：--root → 自工作目录逐级向上发现 .archify/ → 配置文件 roots。
// 三档互斥（命中即停），避免同一产物库被重复登记成两份。
func Load(opt Options) (*Config, error) {
	if opt.Width == 0 {
		opt.Width = defaultWidth
	}
	if opt.Height == 0 {
		opt.Height = defaultHeight
	}

	cfg := &Config{GUI: GUI{Addr: DefaultGUIAddr, Width: opt.Width, Height: opt.Height}}

	file, err := loadFile(opt.ConfigPath)
	if err != nil {
		return nil, err
	}
	cfg.ConfigPath = file.path
	if file.loaded {
		if file.data.GUI.Addr != "" {
			cfg.GUI.Addr = file.data.GUI.Addr
		}
		if file.data.GUI.Width > 0 {
			cfg.GUI.Width = file.data.GUI.Width
		}
		if file.data.GUI.Height > 0 {
			cfg.GUI.Height = file.data.GUI.Height
		}
	}
	if opt.Addr != "" {
		cfg.GUI.Addr = opt.Addr
	}

	wd := opt.WorkingDir
	if wd == "" {
		wd, _ = os.Getwd()
	}

	roots, source, err := resolveRoots(opt.ExplicitRoots, wd, file)
	if err != nil {
		return nil, err
	}
	cfg.Roots = roots
	cfg.RootSource = source

	if err := ValidateAddr(cfg.GUI.Addr); err != nil {
		return nil, err
	}
	return cfg, nil
}

func resolveRoots(explicit []string, wd string, file *loadedFile) ([]Root, string, error) {
	if len(explicit) > 0 {
		roots := make([]Root, 0, len(explicit))
		for _, p := range explicit {
			root, err := makeRoot(p, "")
			if err != nil {
				return nil, "", err
			}
			roots = append(roots, root)
		}
		return roots, "flag", nil
	}

	if discovered, ok := DiscoverUp(wd); ok {
		return []Root{discovered}, "discover", nil
	}

	if len(file.data.Roots) > 0 {
		roots := make([]Root, 0, len(file.data.Roots))
		for _, fr := range file.data.Roots {
			root, err := makeRoot(fr.Path, fr.Label)
			if err != nil {
				return nil, "", err
			}
			roots = append(roots, root)
		}
		return roots, "config", nil
	}

	return nil, "none", nil
}

// DiscoverUp 自 start 目录逐级向上查找 <dir>/.archify，命中即返回该目录。
// 只查祖先链、不扫盘，避免把整块盘的产物吸进索引。
func DiscoverUp(start string) (Root, bool) {
	dir := start
	for {
		candidate := filepath.Join(dir, ArtifactDirName)
		if isDir(candidate) {
			return Root{Label: labelFor(dir), Path: candidate}, true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return Root{}, false
		}
		dir = parent
	}
}

// ValidateAddr 要求 addr 是 host:port 且 host 为回环地址；否则返回 error（调用方据此拒绝启动）。
func ValidateAddr(addr string) error {
	host, port, err := splitAddr(addr)
	if err != nil {
		return err
	}
	if port < 0 || port > 65535 {
		return fmt.Errorf("端口非法: %d（0 表示系统分配空闲端口）", port)
	}
	if !IsLoopbackHost(host) {
		return fmt.Errorf("监听地址 %q 不是回环地址：本工具无鉴权，共享监听属 P2 未实现范围（请改用 127.0.0.1）", addr)
	}
	return nil
}

// SplitAddr 暴露 host 供调用方展示。
func SplitAddr(addr string) (string, int, error) { return splitAddr(addr) }

// IsLoopbackHost 判定 localhost / 127.0.0.0/8 / ::1。空 host 不算回环（省略即绑全接口）。
func IsLoopbackHost(host string) bool {
	if host == "" {
		return false
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func splitAddr(addr string) (string, int, error) {
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		return "", 0, fmt.Errorf("监听地址 %q 必须是 host:port 形态: %w", addr, err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return "", 0, fmt.Errorf("监听地址 %q 的端口不是整数: %w", addr, err)
	}
	return host, port, nil
}

func makeRoot(rawPath, label string) (Root, error) {
	if strings.TrimSpace(rawPath) == "" {
		return Root{}, errors.New("产物根路径不能为空")
	}
	abs, err := filepath.Abs(rawPath)
	if err != nil {
		return Root{}, fmt.Errorf("解析产物根 %q 失败: %w", rawPath, err)
	}
	info, err := os.Stat(abs)
	if err != nil {
		return Root{}, fmt.Errorf("产物根 %s 不可访问（启动即失败，不静默跳过）: %w", abs, err)
	}
	if !info.IsDir() {
		return Root{}, fmt.Errorf("产物根 %s 不是目录", abs)
	}
	if label == "" {
		label = labelFor(abs)
	}
	return Root{Label: label, Path: abs}, nil
}

// labelFor 给根起一个能看懂的分组名：路径本身是约定的 .archify 时用它父目录名，
// 否则用它自己的目录名（避免 --root D:/myartifacts 时 label 变成盘符）。
func labelFor(path string) string {
	base := filepath.Base(filepath.Clean(path))
	if base == ArtifactDirName {
		base = filepath.Base(filepath.Dir(filepath.Clean(path)))
	}
	if base == "" || base == "." || base == string(filepath.Separator) {
		return "default"
	}
	return base
}

type loadedFile struct {
	path   string
	loaded bool
	data   configFile
}

func loadFile(explicit string) (*loadedFile, error) {
	candidates := make([]string, 0, 2)
	if explicit != "" {
		abs, err := filepath.Abs(explicit)
		if err != nil {
			return nil, fmt.Errorf("解析配置文件 %q 失败: %w", explicit, err)
		}
		candidates = append(candidates, abs)
	} else {
		candidates = append(candidates, configCandidates()...)
	}

	for _, path := range candidates {
		info, err := os.Stat(path)
		if errors.Is(err, os.ErrNotExist) {
			// 显式 --config 指到不存在的文件是用户意图落空，同样按「启动即失败」口径处理。
			if explicit != "" {
				return nil, fmt.Errorf("配置文件不存在: %s", path)
			}
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("读取配置文件 %s 失败: %w", path, err)
		}
		if info.IsDir() {
			return nil, fmt.Errorf("配置文件路径 %s 是目录", path)
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("读取配置文件 %s 失败: %w", path, err)
		}
		var data configFile
		if err := yaml.Unmarshal(raw, &data); err != nil {
			return nil, fmt.Errorf("配置文件 %s 解析失败: %w", path, err)
		}
		return &loadedFile{path: path, loaded: true, data: data}, nil
	}
	return &loadedFile{}, nil
}

// configCandidates 与 ssh-tunnel-ui 同口径：当前目录优先，其次用户配置目录。
func configCandidates() []string {
	paths := []string{"config.yaml"}
	if dir, err := os.UserConfigDir(); err == nil {
		paths = append(paths, filepath.Join(dir, "archify-viewer", "config.yaml"))
	}
	return paths
}

func isDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}
