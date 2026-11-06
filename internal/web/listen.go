package web

import (
	"fmt"
	"net"
	"strconv"

	"archify-viewer/internal/config"
)

// Bind 是监听面的第二道闸（第一道在 config.ValidateAddr）。
//
// 有意不同于 ssh-tunnel-ui 的「非回环静默回落到 127.0.0.1 并告警」：本工具没有任何鉴权，
// 且共享形态明确划给 P2，因此非回环一律 **返回 error**，由调用方终止进程（design D-3）。
// 宁可启动失败，也不让一个无鉴权的架构库悄悄开到局域网。
func Bind(addr string) (net.Listener, error) {
	host, port, err := config.SplitAddr(addr)
	if err != nil {
		return nil, err
	}
	if !config.IsLoopbackHost(host) {
		return nil, fmt.Errorf("拒绝监听 %q：非回环地址（共享监听属 P2 未实现范围）", addr)
	}
	ln, err := net.Listen("tcp", net.JoinHostPort(host, strconv.Itoa(port)))
	if err != nil {
		return nil, fmt.Errorf("监听 %s 失败（显式端口被占用时不会静默换端口，可去掉 --addr 端口部分改用空闲端口）: %w", addr, err)
	}
	return ln, nil
}
