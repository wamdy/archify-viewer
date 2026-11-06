//go:build !windows

package main

import "log"

// 非 Windows 没有 GUI 子系统产物，控制台天然存在，无需附着。
func ensureConsole() {}

func reportFatal(err error) {
	if err == nil {
		return
	}
	log.Fatal(err)
}
