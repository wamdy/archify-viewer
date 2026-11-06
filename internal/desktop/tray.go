package desktop

// CloseBehavior 是关闭主窗口时的行为，与配置 gui.close_action 对应。
// 借 ssh-tunnel-ui 的三态设计；差别是本机查看器默认「直接隐藏到托盘」，
// 因为没有需要用户确认的后台连接，且首次隐藏会用托盘气泡说明如何恢复。
type CloseBehavior int

const (
	CloseAsk  CloseBehavior = iota // 每次关闭时询问
	CloseTray                      // 隐藏到系统托盘，服务继续运行
	CloseQuit                      // 关闭窗口即退出
)

// ParseCloseBehavior 将配置值转换为关闭行为；未知值按默认隐藏到托盘处理。
func ParseCloseBehavior(s string) CloseBehavior {
	switch s {
	case "ask":
		return CloseAsk
	case "quit":
		return CloseQuit
	default:
		return CloseTray
	}
}

// TrayOptions 描述托盘图标与关闭行为。Behavior 在每次 WM_CLOSE 时读取，
// 因此设置页修改后无需重启即可生效。
type TrayOptions struct {
	Behavior func() CloseBehavior
	OnQuit   func()
	OnRescan func()
}

func (o TrayOptions) behavior() CloseBehavior {
	if o.Behavior == nil {
		return CloseTray
	}
	return o.Behavior()
}

func (o TrayOptions) quit() {
	if o.OnQuit != nil {
		o.OnQuit()
	}
}

func (o TrayOptions) rescan() {
	if o.OnRescan != nil {
		o.OnRescan()
	}
}
