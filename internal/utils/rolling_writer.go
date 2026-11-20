// Package utils 的 rolling_writer 提供按大小滚动的日志写入器。
//
// 它是纯 io.Writer，不依赖标准库 log / Gin 等任何框架，可被两者同时复用：
// 标准库 log.SetOutput 与 gin.DefaultWriter 都能直接指向它。文件按大小阈值
// 滚动，保留有限份历史备份，避免长时间运行把磁盘写满。
package utils

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

// RollingWriter 把字节写入单个日志文件，文件超过 maxBytes 时滚动：
// 当前文件改名为 1 号备份，旧备份依次后移，超出 maxBackups 的最老一份被删除。
// 序号越小越新（app.1.log 是最近一次滚动出去的，app.log 永远是当前文件）。
//
// 所有方法并发安全：标准库 log 与 Gin 的访问日志可能由不同 goroutine 写入。
type RollingWriter struct {
	mu        sync.Mutex
	dir       string
	baseName  string // 当前日志文件名，例如 assetspider.log
	maxBytes  int64  // 单文件字节上限，超过即滚动
	maxBackup int    // 保留的历史备份份数，0 表示不保留

	file *os.File
	size int64 // 当前文件已写入的字节数
}

// NewRollingWriter 打开（不存在则创建）dir/baseName 作为日志文件。
//
// maxSizeBytes 必须大于 0；maxBackups 不能为负；baseName 不能为空。
// 若目标文件已存在，以追加方式打开并把现有大小计入 size，这样进程重启后
// 仍能正确判断「写满」，不会因忘记历史大小而让单个文件无限增长。
func NewRollingWriter(dir, baseName string, maxSizeBytes int64, maxBackups int) (*RollingWriter, error) {
	if maxSizeBytes <= 0 {
		return nil, fmt.Errorf("日志文件大小上限必须大于 0")
	}
	if maxBackups < 0 {
		return nil, fmt.Errorf("日志备份份数不能为负")
	}
	if strings.TrimSpace(baseName) == "" {
		return nil, fmt.Errorf("日志文件名不能为空")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("创建日志目录 %q: %w", dir, err)
	}
	w := &RollingWriter{
		dir: dir, baseName: baseName,
		maxBytes: maxSizeBytes, maxBackup: maxBackups,
	}
	if err := w.open(); err != nil {
		return nil, err
	}
	return w, nil
}

// Write 实现 io.Writer。写入前若「已有内容 + 本次内容」会超过上限，则先滚动。
// 只有当前文件非空时才滚动，避免单条超过上限的日志触发死循环式反复滚动。
func (w *RollingWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.file == nil {
		return 0, fmt.Errorf("日志写入器已关闭")
	}
	if w.size > 0 && w.size+int64(len(p)) > w.maxBytes {
		if err := w.rotate(); err != nil {
			return 0, err
		}
	}
	n, err := w.file.Write(p)
	w.size += int64(n)
	return n, err
}

// Close 关闭当前日志文件；幂等。关闭后再写入会返回错误。
func (w *RollingWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.file == nil {
		return nil
	}
	err := w.file.Close()
	w.file = nil
	return err
}

// Path 返回当前正在写入的日志文件完整路径。
func (w *RollingWriter) Path() string {
	return w.currentPath()
}

// open 以追加模式打开当前日志文件，并用文件现有大小初始化 size。
func (w *RollingWriter) open() error {
	path := w.currentPath()
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("打开日志文件 %q: %w", path, err)
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return fmt.Errorf("读取日志文件信息 %q: %w", path, err)
	}
	w.file = f
	w.size = info.Size()
	return nil
}

// rotate 执行滚动：关闭当前文件 → 顺移并裁剪备份 → 当前文件改名为 1 号备份
// → 打开新的空日志文件。任一步失败都会中止，绝不带着半滚动状态继续写。
func (w *RollingWriter) rotate() error {
	if err := w.file.Close(); err != nil {
		return fmt.Errorf("关闭当前日志文件: %w", err)
	}
	w.file = nil

	if w.maxBackup == 0 {
		// 不保留备份：删除当前文件后重建，等同于清空重写。
		if err := os.Remove(w.currentPath()); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("清理日志文件: %w", err)
		}
		return w.open()
	}

	// 从最老一份往前处理：达到上限的那份被删除，其余向后顺移一位。
	for i := w.maxBackup; i >= 1; i-- {
		src := w.backupPath(i)
		if i == w.maxBackup {
			// 最老的一份已被挤出保留窗口，删除失败也不阻断写入。
			_ = os.Remove(src)
			continue
		}
		if _, err := os.Stat(src); err == nil {
			if err := os.Rename(src, w.backupPath(i+1)); err != nil {
				return fmt.Errorf("滚动备份 %s: %w", src, err)
			}
		}
	}
	if err := os.Rename(w.currentPath(), w.backupPath(1)); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("滚动当前日志: %w", err)
	}
	return w.open()
}

// currentPath 返回当前日志文件的完整路径。
func (w *RollingWriter) currentPath() string {
	return filepath.Join(w.dir, w.baseName)
}

// backupPath 返回第 n 份备份的完整路径（n 从 1 开始，越小越新）。
// 备份在原文件名主干与扩展名之间插入序号，如 app.log → app.1.log。
func (w *RollingWriter) backupPath(n int) string {
	ext := filepath.Ext(w.baseName)
	stem := strings.TrimSuffix(w.baseName, ext)
	return filepath.Join(w.dir, stem+"."+strconv.Itoa(n)+ext)
}
