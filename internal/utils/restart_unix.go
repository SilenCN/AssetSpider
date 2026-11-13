//go:build !windows

package utils

import (
	"os"
	"path/filepath"
	"syscall"
)

// RestartProcess 用同一份可执行文件、同一组命令行参数与环境变量原地重建进程。
//
// 实现方式是 syscall.Exec：它把当前进程的镜像整个替换掉，PID 与工作目录保持不变，
// 因此无论服务由 systemd 托管还是直接用 nohup 启动，都不会出现「退出后没人拉起」
// 的空窗；对 systemd 的 Type=simple 而言进程从未退出，也不会触发 Restart 策略。
//
// 调用前必须先完成收尾（停止后台任务、关闭数据库连接），否则 Exec 会直接丢弃
// 尚未落盘的数据。本函数成功时不返回；返回错误说明无法原地重启，调用方应退出
// 进程，交给外部监督者（systemd 的 Restart=on-failure）兜底。
func RestartProcess() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	// 部署脚本用软链指向带版本号的二进制时，取真实路径再执行，避免重启后仍跑旧目标。
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	return syscall.Exec(exe, os.Args, os.Environ())
}
