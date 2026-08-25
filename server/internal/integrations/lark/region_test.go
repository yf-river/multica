package lark

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

// capturingRoundTripper records the host of every outbound request and
// replies with a canned Lark-style JSON body that satisfies every decode
// path the client takes (token mint, bot info, contact union_id). It lets
// a test assert WHICH open-platform host a call targeted without dialing
// the real public Feishu / Lark domains.
type capturingRoundTripper struct {
	hosts []string
}

func (rt *capturingRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) {
	rt.hosts = append(rt.hosts, r.URL.Host)
	const body = `{"code":0,"msg":"ok","tenant_access_token":"t","expire":7200,` +
		`"bot":{"open_id":"ou_x"},"data":{"user":{"union_id":"on_x"}}}`
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     make(http.Header),
	}, nil
}

// TestRegion_OpenPlatformBaseURL pins the region→host mapping that both
// the REST client and the WS bootstrap depend on.
func TestRegion_OpenPlatformBaseURL(t *testing.T) {
	cases := []struct {
		region Region
		want   string
	}{
		{RegionFeishu, "https://open.feishu.cn"},
		{RegionLark, "https://open.larksuite.com"},
		{Region(""), ""},
		{Region("bogus"), ""},
	}
	for _, tc := range cases {
		if got := tc.region.OpenPlatformBaseURL(); got != tc.want {
			t.Errorf("Region(%q).OpenPlatformBaseURL() = %q, want %q", tc.region, got, tc.want)
		}
	}
}

func TestParseRegionRejectsInvalidStoredValues(t *testing.T) {
	for _, value := range []string{"", "LARK", "intl"} {
		if _, err := ParseRegion(value); err == nil {
			t.Errorf("ParseRegion(%q) succeeded", value)
		}
	}
	for value, want := range map[string]Region{"feishu": RegionFeishu, "lark": RegionLark} {
		if got, err := ParseRegion(value); err != nil || got != want {
			t.Errorf("ParseRegion(%q) = %q, %v; want %q", value, got, err, want)
		}
	}
}

// TestHTTPClient_ResolvesHostFromRegion is the core dual-region guarantee:
// with NO deployment-wide BaseURL override, the open-platform host is
// chosen per call from InstallationCredentials.Region, so Feishu and Lark
// installations served by one process each reach their own cloud.
func TestHTTPClient_ResolvesHostFromRegion(t *testing.T) {
	cases := []struct {
		name   string
		region Region
		host   string
	}{
		{"feishu", RegionFeishu, "open.feishu.cn"},
		{"lark", RegionLark, "open.larksuite.com"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rt := &capturingRoundTripper{}
			// No BaseURL → region resolution governs the host.
			c := NewHTTPAPIClient(HTTPClientConfig{}).(*httpAPIClient)
			c.httpClient = &http.Client{Transport: rt}
			if _, err := c.GetBotInfo(context.Background(), InstallationCredentials{
				AppID: "cli_x", AppSecret: "s", Region: tc.region,
			}); err != nil {
				t.Fatalf("GetBotInfo: %v", err)
			}
			if len(rt.hosts) == 0 {
				t.Fatalf("no requests captured")
			}
			for _, h := range rt.hosts {
				if h != tc.host {
					t.Errorf("request targeted host %q, want %q", h, tc.host)
				}
			}
		})
	}
}

// TestHTTPClient_BaseURLOverridesRegion pins the test / staging seam: an
// explicit cfg.BaseURL forces every region to that host, which is how the
// existing test suite (and MULTICA_LARK_HTTP_BASE_URL) keeps working.
func TestHTTPClient_BaseURLOverridesRegion(t *testing.T) {
	rt := &capturingRoundTripper{}
	c := NewHTTPAPIClient(HTTPClientConfig{BaseURL: "https://override.example.com"}).(*httpAPIClient)
	c.httpClient = &http.Client{Transport: rt}
	if _, err := c.GetBotInfo(context.Background(), InstallationCredentials{
		AppID: "cli_x", AppSecret: "s", Region: RegionLark, // would be larksuite, but override wins
	}); err != nil {
		t.Fatalf("GetBotInfo: %v", err)
	}
	for _, h := range rt.hosts {
		if h != "override.example.com" {
			t.Errorf("override not honored: host=%q, want override.example.com", h)
		}
	}
}

// TestWSEndpoint_ResolvesHostFromRegion pins that the long-conn bootstrap
// POST (/callback/ws/endpoint) also targets the per-installation region
// host when no deployment-wide override is set.
func TestWSEndpoint_ResolvesHostFromRegion(t *testing.T) {
	cases := []struct {
		region Region
		host   string
	}{
		{RegionFeishu, "open.feishu.cn"},
		{RegionLark, "open.larksuite.com"},
	}
	for _, tc := range cases {
		rt := &wsEndpointRoundTripper{}
		f, err := NewHTTPConnectionTokenFetcher(HTTPConnectionTokenConfig{})
		if err != nil {
			t.Fatalf("NewHTTPConnectionTokenFetcher: %v", err)
		}
		f.httpClient = &http.Client{Transport: rt}
		if _, err := f.Endpoint(context.Background(), InstallationCredentials{
			AppID: "cli_x", AppSecret: "s", Region: tc.region,
		}); err != nil {
			t.Fatalf("Endpoint(region=%q): %v", tc.region, err)
		}
		if rt.host != tc.host {
			t.Errorf("ws bootstrap targeted host %q, want %q (region=%q)", rt.host, tc.host, tc.region)
		}
		if rt.path != "/callback/ws/endpoint" {
			t.Errorf("ws bootstrap path = %q, want /callback/ws/endpoint", rt.path)
		}
	}
}

// wsEndpointRoundTripper returns a valid endpointResponse so Endpoint's
// decode succeeds, while recording the host + path it was asked to reach.
type wsEndpointRoundTripper struct {
	host string
	path string
}

func (rt *wsEndpointRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) {
	rt.host = r.URL.Host
	rt.path = r.URL.Path
	const body = `{"code":0,"msg":"ok","data":{"URL":"wss://example/ws?service_id=1&device_id=d",` +
		`"ClientConfig":{"ReconnectCount":1,"ReconnectInterval":120,"ReconnectNonce":30,"PingInterval":120}}}`
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     make(http.Header),
	}, nil
}
