package service

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/SilenCN/AssetSpider/internal/model"
)

// URLRewriter 按站点配置改写资源地址（播放地址、封面图片），
// 只在「读取」时生效，数据库里仍是采集到的原始地址，改配置不会污染已入库数据。
//
// 改写顺序：先替换域名，再拼接解析/代理地址。这样解析服务拿到的是「已换到可用域名」
// 的原始地址，域名迁移与解析跳转可以同时使用。
type URLRewriter struct {
	prefix string // 解析/代理地址，可能包含 {url} 占位符
	domain string // 规范化后的目标域名，形如 https://cdn.example.com
}

// PlayURLRewriter 是 URLRewriter 的旧名（播放地址改写），保留以兼容既有调用与测试。
type PlayURLRewriter = URLRewriter

// NewURLRewriter 由配置项构造改写器；两项都为空时 Enabled() 为 false。
func NewURLRewriter(prefix, domain string) URLRewriter {
	return URLRewriter{
		prefix: strings.TrimSpace(prefix),
		domain: strings.TrimSpace(domain),
	}
}

// NewPlayURLRewriter 构造播放地址改写器。
func NewPlayURLRewriter(parseAPI, playDomain string) PlayURLRewriter {
	return NewURLRewriter(parseAPI, playDomain)
}

// NewPlayURLRewriterFrom 按站点配置构造播放地址改写器。
func NewPlayURLRewriterFrom(src model.Source) PlayURLRewriter {
	return NewURLRewriter(src.ParseAPI, src.PlayDomain)
}

// NewPicURLRewriterFrom 按站点配置构造图片（封面）地址改写器。
func NewPicURLRewriterFrom(src model.Source) URLRewriter {
	return NewURLRewriter(src.PicAPI, src.PicDomain)
}

// Enabled 表示该站点是否需要改写地址。
func (r URLRewriter) Enabled() bool { return r.prefix != "" || r.domain != "" }

// Rewrite 返回改写后的地址；未配置或被改写项为空时原样返回。
func (r URLRewriter) Rewrite(raw string) string {
	u := strings.TrimSpace(raw)
	if u == "" || !r.Enabled() {
		return raw
	}
	if r.domain != "" {
		u = replacePlayDomain(u, r.domain)
	}
	if r.prefix != "" {
		// 支持 {url} 占位符；未提供时按约定直接前缀拼接。
		if strings.Contains(r.prefix, "{url}") {
			u = strings.ReplaceAll(r.prefix, "{url}", u)
		} else {
			u = r.prefix + u
		}
	}
	return u
}

// replacePlayDomain 用 configured 的协议与域名替换 raw 的，保留路径、查询串与锚点。
func replacePlayDomain(raw, configured string) string {
	base, err := url.Parse(configured)
	if err != nil || base.Host == "" {
		return raw
	}
	orig, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	// 相对地址（无协议、无域名）也统一拼到目标域名下，避免产生半截地址。
	p := orig.Path
	if base.Host != "" && p != "" && !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	base.Path = p
	base.RawPath = ""
	base.RawQuery = orig.RawQuery
	base.Fragment = orig.Fragment
	return base.String()
}

// normalizePrefixURL 校验并清理「解析/代理地址」前缀。
// 允许带 {url} 占位符；留空表示不使用。
func normalizePrefixURL(raw, label, example string) (string, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "", nil
	}
	// 占位符先换成临时值再解析，避免 {url} 被当成非法字符。
	probe := strings.ReplaceAll(s, "{url}", "x")
	u, err := url.Parse(probe)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", fmt.Errorf("%s需为完整的 http(s) URL，例如 %s", label, example)
	}
	return s, nil
}

// normalizeDomain 校验并清理目标域名。
// 允许只写域名（自动补 https://），允许带路径前缀；留空表示不替换域名。
func normalizeDomain(raw, label, example string) (string, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "", nil
	}
	if !strings.Contains(s, "://") {
		s = "https://" + s
	}
	s = strings.TrimSuffix(s, "/")
	u, err := url.Parse(s)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", fmt.Errorf("%s需为 http(s) 域名，例如 %s", label, example)
	}
	return s, nil
}

// NormalizeParseAPI 校验并清理视频解析地址。
func NormalizeParseAPI(raw string) (string, error) {
	return normalizePrefixURL(raw, "视频解析地址", "https://jx.example.com/?url=")
}

// NormalizePlayDomain 校验并清理视频播放域名。
func NormalizePlayDomain(raw string) (string, error) {
	return normalizeDomain(raw, "视频播放域名", "https://cdn.example.com")
}

// NormalizePicAPI 校验并清理图片解析/代理地址。
func NormalizePicAPI(raw string) (string, error) {
	return normalizePrefixURL(raw, "图片解析地址", "https://img.example.com/proxy?url=")
}

// NormalizePicDomain 校验并清理图片域名。
func NormalizePicDomain(raw string) (string, error) {
	return normalizeDomain(raw, "图片域名", "https://img.example.com")
}
