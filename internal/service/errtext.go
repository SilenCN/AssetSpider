package service

// 采集失败的原因说明：把底层错误翻译成用户能看懂的一句话。
//
// 页面上的「说明 / 失败原因」直接展示这段文字。只给技术原文（Get "...": context
// deadline exceeded）等于没给原因——用户不知道是站点挂了、域名错了还是被墙。
// 因此这里按错误类型归类，先给人话，再保留原始错误供排查。

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"syscall"
)

// explainCrawlError 返回带原因说明的失败信息；无法归类时原样返回。
func explainCrawlError(err error) string {
	if err == nil {
		return ""
	}
	if title, hint, ok := networkFailure(err); ok {
		return fmt.Sprintf("%s：%s。技术细节：%v", title, hint, err)
	}
	// 接口返回的不是 JSON：常见于站点加了验证/防盗链，或接口地址已经变更。
	if msg := err.Error(); strings.Contains(msg, "解析采集接口 JSON 失败") ||
		strings.Contains(msg, "解析分类接口 JSON 失败") {
		return fmt.Sprintf("接口返回的不是合法 JSON：可能被站点拦截（需要验证 / 防盗链），或接口地址已变更。技术细节：%v", err)
	}
	return err.Error()
}

// networkFailure 按错误链判断网络层失败的具体原因。
func networkFailure(err error) (title, hint string, ok bool) {
	var netErr net.Error
	var dnsErr *net.DNSError
	var certErr *tls.CertificateVerificationError
	var unknownAuth x509.UnknownAuthorityError
	var recordErr tls.RecordHeaderError

	switch {
	case errors.As(err, &dnsErr):
		return "域名解析失败", "站点域名在 DNS 上查不到，请检查域名拼写或本机 DNS 设置", true
	case errors.As(err, &certErr), errors.As(err, &unknownAuth), errors.As(err, &recordErr):
		return "HTTPS 握手失败", "站点证书无效或与域名不匹配，可尝试改用 http 地址", true
	case errors.Is(err, syscall.ECONNREFUSED):
		return "连接被拒绝", "站点端口未开放，或服务已停止", true
	case errors.Is(err, syscall.ECONNRESET), errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF):
		return "连接被中断", "站点在返回完整响应前断开了连接，可能是站点限流或网络不稳定", true
	case errors.Is(err, context.DeadlineExceeded), errors.As(err, &netErr) && netErr.Timeout():
		return "请求超时", "站点在限定时间内没有响应，可能站点不可达、被网络拦截或已停止服务", true
	}
	return "", "", false
}
