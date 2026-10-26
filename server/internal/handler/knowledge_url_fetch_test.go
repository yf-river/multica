package handler

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"
)

func TestKnowledgeURLRejectsInternalAddresses(t *testing.T) {
	for _, raw := range []string{"file:///etc/passwd", "http://localhost/a", "http://x.localhost/", "http://127.0.0.1", "http://10.1.2.3", "http://169.254.169.254", "http://[::1]", "http://[::ffff:127.0.0.1]", "https://user:pass@example.com"} {
		if _, err := parseKnowledgeURL(raw); err == nil {
			t.Errorf("accepted internal URL %s", raw)
		}
	}
	for _, raw := range []string{"100.64.0.1", "192.0.2.1", "198.18.0.1", "2001:db8::1", "64:ff9b::7f00:1", "fc00::1", "fe80::1"} {
		if publicKnowledgeIP(netip.MustParseAddr(raw)) {
			t.Errorf("accepted reserved IP %s", raw)
		}
	}
}

func TestKnowledgeURLPinsValidatedDNS(t *testing.T) {
	var dialed string
	f := knowledgeURLFetcher{
		resolve: func(context.Context, string, string) ([]netip.Addr, error) {
			return []netip.Addr{netip.MustParseAddr("8.8.8.8")}, nil
		},
		dial: func(_ context.Context, _, address string) (net.Conn, error) {
			dialed = address
			a, b := net.Pipe()
			_ = b.Close()
			return a, nil
		},
	}
	connection, err := f.dialPublic(t.Context(), "tcp", "example.com:443")
	if err != nil {
		t.Fatal(err)
	}
	_ = connection.Close()
	if dialed != "8.8.8.8:443" {
		t.Fatalf("DNS was resolved again at dial: %s", dialed)
	}
	dialed = ""
	f.resolve = func(context.Context, string, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("8.8.8.8"), netip.MustParseAddr("127.0.0.1")}, nil
	}
	if _, err := f.dialPublic(t.Context(), "tcp", "example.com:443"); err == nil || dialed != "" {
		t.Fatal("mixed public/private DNS reached dial")
	}
}

func TestKnowledgeURLDownloadBoundaries(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/redirect":
			http.Redirect(w, r, "http://127.0.0.1/private", http.StatusFound)
		case "/large":
			w.Header().Set("Content-Length", "10485761")
		case "/stream":
			w.Header().Set("Content-Type", "text/plain")
			_, _ = w.Write([]byte(strings.Repeat("x", knowledgeDownloadLimit+1)))
		case "/wait":
			<-r.Context().Done()
		default:
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write([]byte("<p>知识库</p>"))
		}
	}))
	defer server.Close()
	// 连接到本测试服务，仍验证下载器交给 dial 的地址已经固定为公网 IP。
	f := knowledgeURLFetcher{
		resolve: func(context.Context, string, string) ([]netip.Addr, error) {
			return []netip.Addr{netip.MustParseAddr("8.8.8.8")}, nil
		},
		dial: func(ctx context.Context, network, address string) (net.Conn, error) {
			if address != "8.8.8.8:80" {
				t.Errorf("unvalidated dial: %s", address)
			}
			return (&net.Dialer{}).DialContext(ctx, network, strings.TrimPrefix(server.URL, "http://"))
		},
	}
	data, extension, err := f.fetch(t.Context(), "http://example.com/ok")
	if err != nil || extension != ".html" || string(data) != "<p>知识库</p>" {
		t.Fatalf("valid content failed: %q %s %v", data, extension, err)
	}
	for _, path := range []string{"redirect", "large", "stream"} {
		if _, _, err := f.fetch(t.Context(), "http://example.com/"+path); err == nil {
			t.Errorf("accepted %s", path)
		}
	}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	if _, _, err := f.fetch(ctx, "http://example.com/wait"); err == nil {
		t.Fatal("timeout was ignored")
	}
}

func TestKnowledgeURIWorkspaceBoundary(t *testing.T) {
	for _, raw := range []string{"viking://resources/workspaces/ws-other/file", "viking://resources/workspaces/ws/../other", "viking://resources/workspaces/ws/%2e%2e/other", "viking://resources/workspaces/ws/%252e%252e/other", "viking://resources/workspaces/ws/file?x=1", "viking://resources/workspaces/ws\\other"} {
		if knowledgeURIAllowed("ws", raw) {
			t.Errorf("accepted outside URI %s", raw)
		}
	}
	if !knowledgeURIAllowed("ws", "viking://resources/workspaces/ws/file") {
		t.Fatal("valid URI rejected")
	}
}
