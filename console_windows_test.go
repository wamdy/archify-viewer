//go:build windows

package main

import "testing"

// 钉住评审 BLOCK-1：STD_* 是负数转 uintptr，输入/输出只差最后一位。
// 写反过一次，而 Go 侧打印仍能侥幸出来（因为 os.Stdout 变量被直接替换、C 层句柄却错配），
// 所以必须对常量本身断言，不能只看「命令行还能打印」。
func TestStdHandleConstantsMatchGoOfficialValues(t *testing.T) {
	cases := []struct {
		name string
		got  uintptr
		want int32
	}{
		{"STD_INPUT_HANDLE", stdInputHandle, -10},
		{"STD_OUTPUT_HANDLE", stdOutputHandle, -11},
		{"STD_ERROR_HANDLE", stdErrorHandle, -12},
	}
	for _, c := range cases {
		if int32(c.got) != c.want {
			t.Errorf("%s = 0x%x（按 int32 解释是 %d），期望 %d", c.name, c.got, int32(c.got), c.want)
		}
	}
	if stdInputHandle == stdOutputHandle || stdErrorHandle == stdOutputHandle || stdInputHandle == stdErrorHandle {
		t.Error("三个标准句柄常量不得相同")
	}
}
