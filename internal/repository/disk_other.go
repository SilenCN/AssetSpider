//go:build !windows

package repository

import (
	"golang.org/x/sys/unix"
)

// availableDiskSpace 返回 path 所在卷对当前调用者可用的字节数。
// Linux / macOS / 其它 Unix 用 statfs 的 Bavail（对普通用户可用的块数）× Bsize。
func availableDiskSpace(path string) (int64, error) {
	var st unix.Statfs_t
	if err := unix.Statfs(path, &st); err != nil {
		return 0, err
	}
	return int64(st.Bavail) * int64(st.Bsize), nil
}
