//go:build linux

// Linux 平台：通过 syscall.Exec 原地换载新二进制（同 PID，监听端口短暂重置）。
package handlers

import (
	"os"
	"syscall"
)

// restartSelf 以 exe 替换当前进程映像继续运行（新版本立即生效）
func restartSelf(exe string) error {
	return syscall.Exec(exe, os.Args, os.Environ())
}
