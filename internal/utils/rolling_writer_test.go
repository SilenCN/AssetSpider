package utils

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// assertFileContains 断言路径存在且内容包含 want。
func assertFileContains(t *testing.T, path, want string) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取 %s 失败: %v", path, err)
	}
	if !strings.Contains(string(b), want) {
		t.Fatalf("%s 内容 = %q，期望包含 %q", path, string(b), want)
	}
}

func TestRollingWriterRotatesBySize(t *testing.T) {
	dir := t.TempDir()
	w, err := NewRollingWriter(dir, "app.log", 12, 2)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	// 每份 6 字节：写满 12 字节但未超限，不应滚动。
	if _, err := w.Write([]byte("aaaaaa")); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("bbbbbb")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "app.1.log")); !os.IsNotExist(err) {
		t.Fatalf("未超限时不应产生备份，stat 结果: %v", err)
	}

	// 再写 6 字节，12+6>12 → 滚动：旧内容进 1 号备份，当前文件只剩本次内容。
	if _, err := w.Write([]byte("cccccc")); err != nil {
		t.Fatal(err)
	}
	assertFileContains(t, filepath.Join(dir, "app.1.log"), "aaaaaabbbbbb")
	assertFileContains(t, filepath.Join(dir, "app.log"), "cccccc")
}

func TestRollingWriterKeepsMaxBackups(t *testing.T) {
	dir := t.TempDir()
	w, err := NewRollingWriter(dir, "app.log", 6, 2)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	// 三条内容各异的写入，每条都触发一次滚动（前一条写满后下一条越界）。
	for _, s := range []string{"first", "second", "third"} {
		if _, err := w.Write([]byte(s)); err != nil {
			t.Fatal(err)
		}
	}
	// 序号越小越新：1 号是 second，2 号是 first，当前是 third。
	assertFileContains(t, filepath.Join(dir, "app.log"), "third")
	assertFileContains(t, filepath.Join(dir, "app.1.log"), "second")
	assertFileContains(t, filepath.Join(dir, "app.2.log"), "first")
	if _, err := os.Stat(filepath.Join(dir, "app.3.log")); !os.IsNotExist(err) {
		t.Fatalf("备份数超过上限 2，stat 结果: %v", err)
	}
}

func TestRollingWriterNoBackup(t *testing.T) {
	dir := t.TempDir()
	w, err := NewRollingWriter(dir, "app.log", 6, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	if _, err := w.Write([]byte("aaaaaa")); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("bbbbbb")); err != nil {
		t.Fatal(err)
	}
	assertFileContains(t, filepath.Join(dir, "app.log"), "bbbbbb")
	// maxBackups=0 时不应产生任何 .N.log 备份。
	matches, err := filepath.Glob(filepath.Join(dir, "app.*.log"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Fatalf("maxBackups=0 却产生了备份: %v", matches)
	}
}

func TestRollingWriterResumeAppendAfterRestart(t *testing.T) {
	dir := t.TempDir()
	first, err := NewRollingWriter(dir, "app.log", 6, 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := first.Write([]byte("aa")); err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	// 重启后重新打开：历史大小应被计入，写入不能从头覆盖旧内容。
	second, err := NewRollingWriter(dir, "app.log", 6, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	if _, err := second.Write([]byte("bb")); err != nil {
		t.Fatal(err)
	}
	assertFileContains(t, filepath.Join(dir, "app.log"), "aabb")
	if _, err := second.Write([]byte("cccc")); err != nil {
		t.Fatal(err)
	}
	// 2+2+4=8>6 → 滚动，重启前的 aa 必须保留在 1 号备份里。
	assertFileContains(t, filepath.Join(dir, "app.1.log"), "aabb")
	assertFileContains(t, filepath.Join(dir, "app.log"), "cccc")
}

func TestRollingWriterConcurrentWrites(t *testing.T) {
	dir := t.TempDir()
	w, err := NewRollingWriter(dir, "app.log", 1<<20, 2)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	const goroutines, each = 8, 100
	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < each; i++ {
				if _, err := w.Write([]byte("x")); err != nil {
					t.Errorf("并发写入失败: %v", err)
					return
				}
			}
		}()
	}
	wg.Wait()

	b, err := os.ReadFile(filepath.Join(dir, "app.log"))
	if err != nil {
		t.Fatal(err)
	}
	if got := bytes.Count(b, []byte("x")); got != goroutines*each {
		t.Fatalf("并发写入丢失：写入 %d 字节，期望 %d", got, goroutines*each)
	}
}

func TestRollingWriterValidationAndClosedWrite(t *testing.T) {
	dir := t.TempDir()
	if _, err := NewRollingWriter(dir, "app.log", 0, 1); err == nil {
		t.Error("大小上限为 0 应报错")
	}
	if _, err := NewRollingWriter(dir, "app.log", 10, -1); err == nil {
		t.Error("备份份数为负应报错")
	}
	if _, err := NewRollingWriter(dir, "  ", 10, 1); err == nil {
		t.Error("空文件名应报错")
	}

	w, err := NewRollingWriter(dir, "app.log", 10, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil { // 幂等
		t.Fatalf("重复关闭应无错误，实际: %v", err)
	}
	if _, err := w.Write([]byte("x")); err == nil {
		t.Error("关闭后写入应报错")
	}
}
