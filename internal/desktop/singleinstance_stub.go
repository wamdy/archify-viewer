//go:build !windows

package desktop

// AcquireGUISingleInstance 在非 Windows 平台不做单实例守卫：gui 形态本就回退系统浏览器，
// 且 tags.yaml 的并发安全由 internal/tags 的文件锁兜底，多开不会丢数据。恒返回 acquired=true。
func AcquireGUISingleInstance() (func(), bool, error) {
	return func() {}, true, nil
}
