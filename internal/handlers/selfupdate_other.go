//go:build !linux

// 非 Linux 平台不支持原地重启（一键更新在入口处已按平台拦截，此实现仅为满足编译）。
package handlers

import "errors"

// restartSelf 非 Linux 平台返回错误
func restartSelf(_ string) error {
	return errors.New("当前平台不支持自动重启")
}
