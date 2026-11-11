//go:build windows

package repository

import (
	"golang.org/x/sys/windows"
)

// availableDiskSpace 返回 path 所在卷对当前调用者可用的字节数。
// 用于大库结构迁移（ALTER TABLE DROP COLUMN / VACUUM 会重写整表）前的安全预检，
// 避免把磁盘写满反过来拖垮服务本身。
func availableDiskSpace(path string) (int64, error) {
	dir, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, err
	}
	var free, total, totalFree uint64
	if err := windows.GetDiskFreeSpaceEx(dir, &free, &total, &totalFree); err != nil {
		return 0, err
	}
	// 用「卷物理总空闲」而非「调用者配额可用」：删列重写整表要落的是真实磁盘
	// 字节，配额视图在有磁盘配额 / 系统占用时可能虚高，导致误判为空间充足。
	// 两者取较小值更保守。
	if free > totalFree {
		return int64(totalFree), nil
	}
	return int64(free), nil
}
