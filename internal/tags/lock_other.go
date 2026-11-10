//go:build !windows

package tags

import (
	"errors"
	"os"
	"syscall"
)

// POSIX 用 flock：锁挂在打开文件描述上，进程退出自动释放；
// 同进程两个独立 open 也互斥，单测可在进程内模拟两个实例。

func openLockFile(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
}

func tryLockFile(f *os.File) (bool, error) {
	err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
		return false, nil
	}
	return false, err
}

func unlockFile(f *os.File) {
	_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
}

// isShareViolation 在 POSIX 上理论上不会被原子写触发（rename 覆盖打开中的目标本就成功），
// 仅为口径统一保留：EACCES/EBUSY 视为可重试的瞬时占用。
func isShareViolation(err error) bool {
	return errors.Is(err, syscall.EACCES) || errors.Is(err, syscall.EBUSY)
}
