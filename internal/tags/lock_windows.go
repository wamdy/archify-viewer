//go:build windows

package tags

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

// Windows 用 LockFileEx 字节范围锁：锁挂在文件句柄上，进程退出由 OS 释放；
// 同进程的两个独立句柄也互斥（不同于 POSIX fcntl 的进程级语义），单测可在进程内模拟两个实例。
// 锁文件由 os.OpenFile 常规打开即可，LOCKFILE_FAIL_IMMEDIATELY 使加锁同步返回（bbolt 同款姿势）。

func openLockFile(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
}

func tryLockFile(f *os.File) (bool, error) {
	var ov windows.Overlapped
	err := windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, &ov)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
		return false, nil
	}
	return false, err
}

func unlockFile(f *os.File) {
	var ov windows.Overlapped
	_ = windows.UnlockFileEx(windows.Handle(f.Fd()), 0, 1, 0, &ov)
}

// isShareViolation 识别 Windows 上「目标仍被非 share-delete 句柄打开」的 rename 失败：
// ERROR_ACCESS_DENIED(5)/ERROR_SHARING_VIOLATION(32)。写侧据此做有界重试（句柄微秒级释放）。
func isShareViolation(err error) bool {
	return errors.Is(err, windows.ERROR_ACCESS_DENIED) || errors.Is(err, windows.ERROR_SHARING_VIOLATION)
}
