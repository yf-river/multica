package handler

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"

	"golang.org/x/text/encoding/htmlindex"
	"golang.org/x/text/transform"
)

const knowledgeDownloadLimit = 10 << 20

var forbiddenKnowledgeNetworks = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"), netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"), netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"), netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"), netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("64:ff9b::/96"), netip.MustParsePrefix("64:ff9b:1::/48"),
	netip.MustParsePrefix("2001::/32"), netip.MustParsePrefix("2001:db8::/32"),
	netip.MustParsePrefix("2002::/16"),
}

func publicKnowledgeIP(ip netip.Addr) bool {
	ip = ip.Unmap()
	if !ip.IsValid() || !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
		return false
	}
	for _, network := range forbiddenKnowledgeNetworks {
		if network.Contains(ip) {
			return false
		}
	}
	return true
}

func parseKnowledgeURL(raw string) (*url.URL, error) {
	u, err := url.ParseRequestURI(strings.TrimSpace(raw))
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.Opaque != "" {
		return nil, errors.New("请输入不含账号密码的 HTTP 或 HTTPS 地址")
	}
	host := strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
	if host == "localhost" || strings.HasSuffix(host, ".localhost") || strings.Contains(host, "%") {
		return nil, errors.New("不能导入本机或内部网络地址")
	}
	if ip, e := netip.ParseAddr(host); e == nil && !publicKnowledgeIP(ip) {
		return nil, errors.New("不能导入本机或内部网络地址")
	}
	u.Fragment = ""
	return u, nil
}

type knowledgeURLFetcher struct {
	resolve func(context.Context, string, string) ([]netip.Addr, error)
	dial    func(context.Context, string, string) (net.Conn, error)
}

func newKnowledgeURLFetcher() knowledgeURLFetcher {
	dialer := &net.Dialer{Timeout: 10 * time.Second}
	return knowledgeURLFetcher{resolve: net.DefaultResolver.LookupNetIP, dial: dialer.DialContext}
}

// 每次连接解析并固定到已验证的公网 IP；HTTP 客户端不继承宿主代理。
func (f knowledgeURLFetcher) dialPublic(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, errors.New("下载地址无效")
	}
	ips, err := f.resolve(ctx, "ip", host)
	if err != nil || len(ips) == 0 {
		return nil, errors.New("无法解析下载地址")
	}
	for _, ip := range ips {
		if !publicKnowledgeIP(ip) {
			return nil, errors.New("不能导入本机或内部网络地址")
		}
	}
	for _, ip := range ips {
		connection, e := f.dial(ctx, network, net.JoinHostPort(ip.Unmap().String(), port))
		if e == nil {
			return connection, nil
		}
		err = e
	}
	return nil, errors.New("无法连接下载地址")
}

func (f knowledgeURLFetcher) fetch(ctx context.Context, raw string) ([]byte, string, error) {
	u, err := parseKnowledgeURL(raw)
	if err != nil {
		return nil, "", err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	transport := &http.Transport{Proxy: nil, DialContext: f.dialPublic, TLSHandshakeTimeout: 10 * time.Second, ResponseHeaderTimeout: 10 * time.Second, MaxResponseHeaderBytes: 1 << 20}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, CheckRedirect: func(next *http.Request, via []*http.Request) error {
		if len(via) > 5 {
			return errors.New("下载地址重定向次数过多")
		}
		if _, err := parseKnowledgeURL(next.URL.String()); err != nil {
			return err
		}
		next.Header.Del("Referer")
		return nil
	}}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, "", errors.New("下载地址无效")
	}
	request.Header.Set("User-Agent", "Multica-Knowledge/1.0")
	response, err := client.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return nil, "", errors.New("下载超时或已取消")
		}
		// url.Error 含完整 URL，可能带签名参数；只返回下载阶段的通用错误。
		return nil, "", errors.New("下载失败，请检查地址和网络访问权限")
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, "", fmt.Errorf("下载服务器返回 HTTP %d", response.StatusCode)
	}
	if response.ContentLength > knowledgeDownloadLimit {
		return nil, "", errors.New("下载内容超过 10 MiB")
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, knowledgeDownloadLimit+1))
	if err != nil {
		return nil, "", errors.New("读取下载内容失败")
	}
	if len(data) > knowledgeDownloadLimit {
		return nil, "", errors.New("下载内容超过 10 MiB")
	}
	contentType, params, _ := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if contentType == "" || contentType == "application/octet-stream" {
		contentType = http.DetectContentType(data)
	}
	extension := ""
	switch contentType {
	case "text/html", "application/xhtml+xml":
		extension = ".html"
	case "text/plain":
		extension = ".txt"
	case "text/markdown", "text/x-markdown":
		extension = ".md"
	case "application/pdf":
		extension = ".pdf"
	default:
		return nil, "", errors.New("暂不支持此内容类型，请导入网页、文本、Markdown 或 PDF")
	}
	if extension != ".pdf" && params["charset"] != "" && !strings.EqualFold(params["charset"], "utf-8") {
		encoding, e := htmlindex.Get(params["charset"])
		if e != nil {
			return nil, "", errors.New("网页字符编码不受支持")
		}
		data, _, err = transform.Bytes(encoding.NewDecoder(), data)
		if err != nil || len(data) > knowledgeDownloadLimit {
			return nil, "", errors.New("网页转码失败或内容过大")
		}
	}
	return data, extension, nil
}
