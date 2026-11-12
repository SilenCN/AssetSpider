package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"syscall"
	"testing"
)

// TestExplainCrawlError 验证失败原因会翻译成人话，同时保留原始错误。
//
// 页面上的失败原因直接取这段文字：只说 "context deadline exceeded" 用户无法判断
// 是站点挂了、域名写错还是被网络拦截；只说 "请求超时" 又不利于排查。
func TestExplainCrawlError(t *testing.T) {
	timeoutErr := fmt.Errorf("第 1 页采集失败: 请求采集接口失败: %w",
		fmt.Errorf("Get %q: %w", "https://f-1.cc/api.php", context.DeadlineExceeded))
	dnsErr := fmt.Errorf("请求采集接口失败: %w", &net.DNSError{Err: "no such host", Name: "nope.example"})
	refusedErr := fmt.Errorf("请求采集接口失败: %w",
		&net.OpError{Op: "dial", Net: "tcp", Err: os.NewSyscallError("connect", syscall.ECONNREFUSED)})
	eofErr := fmt.Errorf("读取采集响应失败: %w", io.ErrUnexpectedEOF)

	cases := []struct {
		name  string
		err   error
		wants []string
	}{
		{"超时", timeoutErr, []string{"请求超时", "技术细节", "context deadline exceeded"}},
		{"域名解析", dnsErr, []string{"域名解析失败", "DNS", "no such host"}},
		{"连接被拒绝", refusedErr, []string{"连接被拒绝", "connection refused"}},
		{"连接被中断", eofErr, []string{"连接被中断"}},
	}
	for _, c := range cases {
		got := explainCrawlError(c.err)
		for _, w := range c.wants {
			if !strings.Contains(got, w) {
				t.Errorf("%s: 输出 %q 缺少 %q", c.name, got, w)
			}
		}
	}

	// 非网络类错误原样返回，不能凭空编原因。
	plain := errors.New("采集接口返回 HTTP 403")
	if got := explainCrawlError(plain); got != plain.Error() {
		t.Errorf("普通错误被改写: %q", got)
	}
	// 接口返回非 JSON 时给出「被拦截 / 地址变更」的提示。
	jsonErr := errors.New("解析采集接口 JSON 失败: invalid character '<'")
	if got := explainCrawlError(jsonErr); !strings.Contains(got, "不是合法 JSON") {
		t.Errorf("JSON 解析错误未给出可操作提示: %q", got)
	}
	if got := explainCrawlError(nil); got != "" {
		t.Errorf("nil 错误应返回空串，实际 %q", got)
	}
}
