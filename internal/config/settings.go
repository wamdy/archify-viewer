package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"
)

// SettingsRoot 是设置页里的一处产物根。Path 是**用户配置文件里写的原样字符串**，
// 不是 index 扫描得到的内部绝对路径：/api/index 依旧不含任何路径（红线 4），
// 设置页要能管理目录就必须看得见用户自己填的值。
type SettingsRoot struct {
	Path   string `json:"path"`
	Label  string `json:"label"`
	Origin string `json:"origin"` // flag | discover | config | none
}

// SettingsView 是 GET /api/settings 与两个写端点共同返回的快照，
// 前端据此一次刷新目录列表、关闭行为与来源提示，避免多处状态各自缓存。
type SettingsView struct {
	Roots       []SettingsRoot `json:"roots"`
	Source      string         `json:"source"`
	ConfigPath  string         `json:"config_path"`
	ListenAddr  string         `json:"listen_addr"`
	CloseAction string         `json:"close_action"`
	Managed     bool           `json:"managed"`  // 由 -root / 自动发现接管，重启后优先级可能覆盖配置
	Editable    bool           `json:"editable"` // 始终可编辑：改动会立即作用于当前会话
}

const (
	maxRoots    = 32
	maxPathLen  = 4096
	maxBodyHint = "路径过长"
)

// Editor 是设置页的写面：所有配置文件写入都收敛在这里（web 包只调接口方法）。
// 保存采用「临时文件 + 原子改名」，权限 0600，与 ssh-tunnel-ui 的 config.Save 同口径。
type Editor struct {
	mu       sync.Mutex
	cfg      *Config
	path     string
	data     configFile // 原始文件内容，保存时保留用户手写的 gui 字段
	raw      []fileRoot
	fromFile bool
	origin   string
}

// NewEditor 构造设置编辑器。explicitPath 为空时按「已加载的配置 → 用户配置目录」择一。
func NewEditor(cfg *Config, explicitPath string) (*Editor, error) {
	if cfg == nil {
		return nil, errors.New("配置未初始化")
	}
	path := strings.TrimSpace(explicitPath)
	if path == "" {
		path = cfg.ConfigPath
	}
	if path == "" {
		var err error
		path, err = UserConfigPath()
		if err != nil {
			return nil, err
		}
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("解析配置文件路径失败: %w", err)
	}

	e := &Editor{cfg: cfg, path: abs, origin: cfg.Source()}
	if e.origin == "" {
		e.origin = "none"
	}

	if raw, err := os.ReadFile(abs); err == nil {
		var data configFile
		if err := yaml.Unmarshal(raw, &data); err != nil {
			return nil, fmt.Errorf("配置文件 %s 解析失败: %w", abs, err)
		}
		e.data = data
		e.raw = append([]fileRoot(nil), data.Roots...)
		e.fromFile = true
		if len(e.raw) > 0 {
			e.origin = "config"
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("读取配置文件 %s 失败: %w", abs, err)
	}

	// 没有配置文件时，把当前会话的根作为可编辑初值（用户点「添加」前先能看清现状）。
	if len(e.raw) == 0 {
		for _, r := range cfg.RootSnapshot() {
			e.raw = append(e.raw, fileRoot{Path: r.Path, Label: r.Label})
		}
	}
	return e, nil
}

// Path 返回实际使用的配置文件路径（供日志与设置页展示）。
func (e *Editor) Path() string { return e.path }

// Settings 返回当前快照。
func (e *Editor) Settings() SettingsView {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.snapshot()
}

func (e *Editor) snapshot() SettingsView {
	roots := make([]SettingsRoot, 0, len(e.raw))
	for _, r := range e.raw {
		roots = append(roots, SettingsRoot{Path: r.Path, Label: r.Label, Origin: e.origin})
	}
	return SettingsView{
		Roots:       roots,
		Source:      e.cfg.Source(),
		ConfigPath:  e.path,
		ListenAddr:  e.cfg.GUIValues().Addr,
		CloseAction: e.cfg.CloseActionValue(),
		Managed:     e.cfg.Source() == "flag" || e.cfg.Source() == "discover",
		Editable:    true,
	}
}

// AddRoot 校验并新增一处产物根：目录必须存在、长度受限、按绝对路径去重。
func (e *Editor) AddRoot(path string) (SettingsView, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return SettingsView{}, errors.New("目录路径不能为空")
	}
	if len(path) > maxPathLen {
		return SettingsView{}, errors.New(maxBodyHint)
	}
	r, err := makeRoot(path, "")
	if err != nil {
		return SettingsView{}, err
	}

	e.mu.Lock()
	defer e.mu.Unlock()
	if len(e.raw) >= maxRoots {
		return e.snapshot(), fmt.Errorf("最多配置 %d 处产物根", maxRoots)
	}
	want := canonical(path)
	taken := make(map[string]bool, len(e.raw))
	for _, r := range e.raw {
		if canonical(r.Path) == want {
			return e.snapshot(), errors.New("该目录已在列表中")
		}
		taken[r.Label] = true
	}
	next := append(append([]fileRoot(nil), e.raw...), fileRoot{Path: path, Label: uniqueLabel(r.Label, taken)})
	return e.applyLocked(next, e.cfg.CloseActionValue())
}

// RenameRoot 修改某处产物根的显示名（空名回落为默认名；重名自动退避）。
func (e *Editor) RenameRoot(path, label string) (SettingsView, error) {
	label = strings.TrimSpace(label)
	if len(label) > maxPathLen {
		return SettingsView{}, errors.New(maxBodyHint)
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	want := canonical(path)
	idx := -1
	taken := make(map[string]bool, len(e.raw))
	for i, r := range e.raw {
		if canonical(r.Path) == want {
			idx = i
			continue
		}
		taken[r.Label] = true
	}
	if idx < 0 {
		return e.snapshot(), errors.New("目录不在列表中")
	}
	if label == "" {
		label = labelFor(e.raw[idx].Path)
	}
	next := append([]fileRoot(nil), e.raw...)
	next[idx].Label = uniqueLabel(label, taken)
	return e.applyLocked(next, e.cfg.CloseActionValue())
}

// uniqueLabel 重名退避：base、base-2、base-3……
func uniqueLabel(base string, taken map[string]bool) string {
	base = strings.TrimSpace(base)
	if base == "" {
		base = "未命名"
	}
	if !taken[base] {
		return base
	}
	for i := 2; i < 1000; i++ {
		cand := fmt.Sprintf("%s-%d", base, i)
		if !taken[cand] {
			return cand
		}
	}
	return base + "-" + fmt.Sprint(len(taken)+1)
}

// RemoveRoot 移除一处产物根；允许全部删空（空库由界面引导）。
func (e *Editor) RemoveRoot(path string) (SettingsView, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	want := canonical(strings.TrimSpace(path))
	next := make([]fileRoot, 0, len(e.raw))
	found := false
	for _, r := range e.raw {
		if !found && canonical(r.Path) == want {
			found = true
			continue
		}
		next = append(next, r)
	}
	if !found {
		return e.snapshot(), errors.New("要移除的目录不在列表中")
	}
	return e.applyLocked(next, e.cfg.CloseActionValue())
}

// SetCloseAction 更新关闭行为并持久化；未知值由 NormalizeCloseAction 收敛。
func (e *Editor) SetCloseAction(value string) (SettingsView, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	v := NormalizeCloseAction(value)
	return e.applyLocked(append([]fileRoot(nil), e.raw...), v)
}

// applyLocked 先落盘再改运行态：写失败时当前会话保持不变，避免「界面显示成功、重启却回退」。
func (e *Editor) applyLocked(roots []fileRoot, closeAction string) (SettingsView, error) {
	if err := e.saveLocked(roots, closeAction); err != nil {
		return e.snapshot(), err
	}

	cfgRoots := make([]Root, 0, len(roots))
	for _, r := range roots {
		root, err := makeRoot(r.Path, r.Label)
		if err != nil {
			// 落盘已成功但某个根在瞬间变得不可访问：仍按规则码交给 index.Scan 处理，
			// 不让整个设置操作回滚，也不让进程崩掉。
			cfgRoots = append(cfgRoots, Root{Label: r.Label, Path: r.Path})
			continue
		}
		cfgRoots = append(cfgRoots, root)
	}
	e.raw = append([]fileRoot(nil), roots...)
	e.fromFile = true
	e.origin = "config"
	e.cfg.SetRoots(cfgRoots, "config")
	e.cfg.SetCloseAction(closeAction)
	return e.snapshot(), nil
}

func (e *Editor) saveLocked(roots []fileRoot, closeAction string) error {
	data := e.data
	data.Roots = roots
	data.GUI.CloseAction = closeAction

	out, err := yaml.Marshal(&data)
	if err != nil {
		return fmt.Errorf("序列化配置失败: %w", err)
	}
	dir := filepath.Dir(e.path)
	if dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return fmt.Errorf("创建配置目录 %s 失败: %w", dir, err)
		}
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(e.path)+".tmp")
	if err != nil {
		return fmt.Errorf("创建临时配置文件失败: %w", err)
	}
	tmpName := tmp.Name()
	defer func() {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
	}()
	if _, err := tmp.Write(out); err != nil {
		return fmt.Errorf("写入配置文件失败: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		return fmt.Errorf("写入配置文件失败: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("写入配置文件失败: %w", err)
	}
	if err := os.Rename(tmpName, e.path); err != nil {
		return fmt.Errorf("写入配置文件 %s 失败: %w", e.path, err)
	}
	_ = os.Chmod(e.path, 0o600) // Windows 上是 no-op，忽略错误
	e.data = data
	return nil
}

// canonical 用于按绝对路径去重/匹配（Windows 大小写不敏感）。
func canonical(path string) string {
	if path == "" {
		return ""
	}
	if abs, err := filepath.Abs(path); err == nil {
		path = abs
	}
	return strings.ToLower(filepath.Clean(path))
}
