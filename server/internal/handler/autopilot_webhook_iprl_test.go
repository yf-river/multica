package handler

import (
	"net/http/httptest"
	"net/netip"
	"testing"
)

func newClientIPHandler(t *testing.T, cidrs ...string) *Handler {
	t.Helper()
	var prefixes []netip.Prefix
	for _, c := range cidrs {
		p, err := netip.ParsePrefix(c)
		if err != nil {
			t.Fatalf("bad test CIDR %q: %v", c, err)
		}
		prefixes = append(prefixes, p)
	}
	return &Handler{cfg: Config{TrustedProxies: prefixes}}
}

func TestClientIPForRateLimit(t *testing.T) {
	tests := []struct {
		name       string
		cidrs      []string
		remoteAddr string
		xff        string
		xRealIP    string
		want       string
	}{
		{name: "proxy headers disabled", remoteAddr: "203.0.113.5:1234", xff: "1.2.3.4", xRealIP: "5.6.7.8", want: "203.0.113.5"},
		{name: "trusted proxy XFF", cidrs: []string{"10.0.0.0/8"}, remoteAddr: "10.1.2.3:9999", xff: "5.5.5.5", want: "5.5.5.5"},
		{name: "untrusted proxy XFF", cidrs: []string{"10.0.0.0/8"}, remoteAddr: "203.0.113.5:1234", xff: "5.5.5.5", want: "203.0.113.5"},
		{name: "multi-hop XFF", cidrs: []string{"10.0.0.0/8"}, remoteAddr: "10.1.2.3:9999", xff: "5.5.5.5, 10.0.0.7", want: "5.5.5.5"},
		{name: "X-Real-IP fallback", cidrs: []string{"10.0.0.0/8"}, remoteAddr: "10.1.2.3:9999", xRealIP: "7.7.7.7", want: "7.7.7.7"},
		{name: "trusted IPv6", cidrs: []string{"::1/128"}, remoteAddr: "[::1]:5000", xff: "9.9.9.9", want: "9.9.9.9"},
		{name: "untrusted IPv6", cidrs: []string{"10.0.0.0/8"}, remoteAddr: "[2001:db8::1]:5000", xff: "9.9.9.9", want: "2001:db8::1"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			h := newClientIPHandler(t, test.cidrs...)
			req := httptest.NewRequest("POST", "/", nil)
			req.RemoteAddr = test.remoteAddr
			req.Header.Set("X-Forwarded-For", test.xff)
			req.Header.Set("X-Real-IP", test.xRealIP)
			if got := h.clientIPForRateLimit(req); got != test.want {
				t.Fatalf("clientIPForRateLimit() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestRemoteAddrHost(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"203.0.113.5:1234", "203.0.113.5"},
		{"[::1]:8080", "::1"},
		{"[2001:db8::1]:443", "2001:db8::1"},
		{"203.0.113.5", "203.0.113.5"},
		{"2001:db8::1", "2001:db8::1"},
		{"", ""},
	}
	for _, tc := range cases {
		if got := remoteAddrHost(tc.in); got != tc.want {
			t.Errorf("remoteAddrHost(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
