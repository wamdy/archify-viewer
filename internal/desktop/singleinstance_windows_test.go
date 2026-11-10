//go:build windows

package desktop

import (
	"fmt"
	"testing"
	"time"
)

// TestAcquireNamedMutexExcludesSecondHolder 用唯一名验证命名 mutex 的进程内互斥语义：
// 首取成功→二取被拒→释放后可再取。唯一名避免与真机上正在运行的 gui 实例撞名。
func TestAcquireNamedMutexExcludesSecondHolder(t *testing.T) {
	name := fmt.Sprintf(`Local\archify-viewer-test-%d`, time.Now().UnixNano())

	release, ok, err := acquireNamedMutex(name)
	if err != nil || !ok {
		t.Fatalf("首次抢占应成功: ok=%v err=%v", ok, err)
	}

	_, ok2, err2 := acquireNamedMutex(name)
	if err2 != nil {
		t.Fatalf("第二次抢占不应报系统错误: %v", err2)
	}
	if ok2 {
		t.Error("已有实例持锁时第二次抢占应被拒（acquired=false）")
	}

	release()

	release3, ok3, err3 := acquireNamedMutex(name)
	if err3 != nil || !ok3 {
		t.Fatalf("释放后应可再次抢占: ok=%v err=%v", ok3, err3)
	}
	release3()
}
