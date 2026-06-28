package daemon

import (
	"context"
	"encoding/json"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/multica-ai/multica/server/pkg/taskfailure"
)

const (
	runtimeNetworkHealthy     = "healthy"
	runtimeNetworkDegraded    = "degraded"
	runtimeNetworkUnavailable = "unavailable"
	runtimeNetworkNotChecked  = "not_checked"

	runtimeNetworkCheckTimeout = 20 * time.Second
)

var checkRuntimeNetwork = func(d *Daemon, ctx context.Context, provider string, entry AgentEntry) runtimeNetworkHealth {
	return d.checkProviderNetwork(ctx, provider, entry)
}

type runtimeNetworkHealth struct {
	Status           string `json:"status"`
	CheckedAt        string `json:"checked_at"`
	Provider         string `json:"provider"`
	ProxyMode        string `json:"proxy_mode,omitempty"`
	ProxyURLRedacted string `json:"proxy_url_redacted,omitempty"`
	NoProxy          string `json:"no_proxy,omitempty"`
	FailureHint      string `json:"failure_hint,omitempty"`
	Error            string `json:"error,omitempty"`
	Check            string `json:"check,omitempty"`
}

func runtimeNetworkKey(provider, profileID string) string {
	return strings.TrimSpace(provider) + "\x00" + strings.TrimSpace(profileID)
}

func runtimeNetworkStatusBlocksClaims(status string) bool {
	return status == runtimeNetworkUnavailable
}

func (d *Daemon) checkProviderNetwork(ctx context.Context, provider string, entry AgentEntry) runtimeNetworkHealth {
	health := runtimeNetworkHealth{
		Status:    runtimeNetworkNotChecked,
		CheckedAt: time.Now().UTC().Format(time.RFC3339),
		Provider:  provider,
	}
	proxy := runtimeProxyConfigForProvider(provider)
	health.ProxyMode = proxy.mode
	health.ProxyURLRedacted = redactProxyURL(proxy.url)
	health.NoProxy = proxy.noProxy

	if provider != "codex" {
		health.FailureHint = "network preflight is not implemented for this provider"
		return health
	}

	execPath := strings.TrimSpace(entry.Path)
	if execPath == "" {
		execPath = "codex"
	}
	runCtx, cancel := context.WithTimeout(ctx, runtimeNetworkCheckTimeout)
	defer cancel()
	cmd := exec.CommandContext(runCtx, execPath, "debug", "models")
	cmd.Env = applyRuntimeProxyEnv(os.Environ(), provider)
	out, err := cmd.CombinedOutput()
	health.Check = "codex debug models"
	if err != nil {
		health.Status = runtimeNetworkUnavailable
		health.Error = strings.TrimSpace(string(out))
		if health.Error == "" {
			health.Error = err.Error()
		}
		health.FailureHint = "Codex model catalog check failed. Check this runner's local proxy, CODEX_HOME login state, and whether the proxy supports TLS/WebSocket traffic."
		return health
	}
	health.Status = runtimeNetworkHealthy
	return health
}

func (d *Daemon) markRuntimeNetworkFailure(runtimeID string, reason taskfailure.Reason, errText string) {
	if reason != taskfailure.ReasonAgentProviderNetwork || strings.TrimSpace(runtimeID) == "" {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.runtimeNetworkByID == nil {
		d.runtimeNetworkByID = make(map[string]runtimeNetworkHealth)
	}
	if d.runtimeNetworkDirtyID == nil {
		d.runtimeNetworkDirtyID = make(map[string]struct{})
	}
	rt := d.runtimeIndex[runtimeID]
	provider := rt.Provider
	health := runtimeNetworkHealth{
		Status:      runtimeNetworkUnavailable,
		CheckedAt:   time.Now().UTC().Format(time.RFC3339),
		Provider:    provider,
		FailureHint: "Provider network failed during task execution. Check this runner's local proxy and restart or re-register the daemon after fixing it.",
		Error:       truncateRuntimeNetworkError(errText),
	}
	proxy := runtimeProxyConfigForProvider(provider)
	health.ProxyMode = proxy.mode
	health.ProxyURLRedacted = redactProxyURL(proxy.url)
	health.NoProxy = proxy.noProxy
	d.runtimeNetworkByID[runtimeID] = health
	d.runtimeNetworkDirtyID[runtimeID] = struct{}{}
}

func (d *Daemon) runtimeNetworkBlocksClaims(runtimeID string) (runtimeNetworkHealth, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	health, ok := d.runtimeNetworkByID[runtimeID]
	if !ok {
		return runtimeNetworkHealth{}, false
	}
	return health, runtimeNetworkStatusBlocksClaims(health.Status)
}

func (d *Daemon) recordRuntimeNetworkHealth(runtimeID string, health runtimeNetworkHealth) {
	if runtimeID == "" {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.runtimeNetworkByID == nil {
		d.runtimeNetworkByID = make(map[string]runtimeNetworkHealth)
	}
	d.runtimeNetworkByID[runtimeID] = health
}

func (d *Daemon) runtimeNetworkHeartbeatMetadata(runtimeID string) (json.RawMessage, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if _, dirty := d.runtimeNetworkDirtyID[runtimeID]; !dirty {
		return nil, false
	}
	health, ok := d.runtimeNetworkByID[runtimeID]
	if !ok {
		return nil, false
	}
	return runtimeNetworkMetadata(health), true
}

func (d *Daemon) clearRuntimeNetworkHeartbeatMetadata(runtimeID string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.runtimeNetworkDirtyID, runtimeID)
}

func runtimeNetworkMetadata(health runtimeNetworkHealth) json.RawMessage {
	raw, _ := json.Marshal(map[string]any{
		"network": health,
	})
	return raw
}

type runtimeProxyConfig struct {
	mode    string
	url     string
	noProxy string
}

func runtimeProxyConfigForProvider(provider string) runtimeProxyConfig {
	prefix := "MULTICA_" + strings.ToUpper(strings.TrimSpace(provider)) + "_"
	rawURL := firstEnv(prefix+"PROXY_URL", "MULTICA_RUNTIME_PROXY_URL")
	mode := strings.ToLower(firstEnv(prefix+"PROXY_MODE", "MULTICA_RUNTIME_PROXY_MODE"))
	if mode == "" {
		if isDirectProxyValue(rawURL) {
			mode = "direct"
		} else if rawURL != "" {
			mode = "proxy"
		} else {
			mode = "auto"
		}
	}
	if isDirectProxyValue(rawURL) {
		rawURL = ""
		mode = "direct"
	}
	return runtimeProxyConfig{
		mode:    mode,
		url:     rawURL,
		noProxy: firstEnv(prefix+"NO_PROXY", "MULTICA_RUNTIME_NO_PROXY", "NO_PROXY", "no_proxy"),
	}
}

func applyRuntimeProxyEnv(base []string, provider string) []string {
	proxy := runtimeProxyConfigForProvider(provider)
	if proxy.mode == "auto" || (proxy.mode == "" && proxy.url == "") {
		return base
	}
	out := make([]string, 0, len(base)+8)
	for _, entry := range base {
		key, _, _ := strings.Cut(entry, "=")
		switch key {
		case "HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "http_proxy", "https_proxy", "all_proxy", "NO_PROXY", "no_proxy":
			continue
		}
		out = append(out, entry)
	}
	if proxy.mode == "direct" || proxy.url == "" {
		return out
	}
	for _, key := range []string{"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "http_proxy", "https_proxy", "all_proxy"} {
		out = append(out, key+"="+proxy.url)
	}
	if proxy.noProxy != "" {
		out = append(out, "NO_PROXY="+proxy.noProxy, "no_proxy="+proxy.noProxy)
	}
	return out
}

func mergeRuntimeProxyEnv(extra map[string]string, provider string) {
	proxy := runtimeProxyConfigForProvider(provider)
	if proxy.mode == "auto" || (proxy.mode == "" && proxy.url == "") {
		return
	}
	for _, key := range []string{"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "http_proxy", "https_proxy", "all_proxy", "NO_PROXY", "no_proxy"} {
		delete(extra, key)
	}
	if proxy.mode == "direct" || proxy.url == "" {
		return
	}
	for _, key := range []string{"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "http_proxy", "https_proxy", "all_proxy"} {
		extra[key] = proxy.url
	}
	if proxy.noProxy != "" {
		extra["NO_PROXY"] = proxy.noProxy
		extra["no_proxy"] = proxy.noProxy
	}
}

func firstEnv(keys ...string) string {
	for _, key := range keys {
		if value := strings.TrimSpace(os.Getenv(key)); value != "" {
			return value
		}
	}
	return ""
}

func isDirectProxyValue(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "direct", "none", "no", "off", "false", "0", "disable", "disabled":
		return true
	default:
		return false
	}
}

func redactProxyURL(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return ""
	}
	u, err := url.Parse(trimmed)
	if err != nil {
		return trimmed
	}
	if u.User != nil {
		u.User = url.User("redacted")
	}
	return u.String()
}

func truncateRuntimeNetworkError(value string) string {
	value = strings.TrimSpace(value)
	if len(value) <= 500 {
		return value
	}
	return value[:500]
}
