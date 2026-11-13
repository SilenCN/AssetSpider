//go:build windows

package utils

import "errors"

// RestartProcess 在 Windows 上不提供原地重建进程的能力（syscall.Exec 不可用）。
// 调用方拿到错误后会退出进程，由外部监督者重新拉起。
func RestartProcess() error {
	return errors.New("当前平台不支持原地重启，请手动重启服务")
}
