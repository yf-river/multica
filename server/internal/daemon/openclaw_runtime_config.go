package daemon

import (
	"encoding/json"
	"fmt"

	"github.com/multica-ai/multica/server/internal/daemon/execenv"
)

// openclawRuntimeConfig is the schema the daemon expects under an openclaw
// agent's `runtime_config` JSONB column. An absent mode is normalized to
// local at this boundary so execution only sees local or gateway.
//
// Schema (issue #3260):
//
//	{
//	  "mode": "local" | "gateway",     // default: "local"
//	  "gateway": {
//	    "host":  "<hostname>",         // remote OpenClaw gateway host
//	    "port":  18789,                // gateway port
//	    "token": "<bearer>",           // gateway auth token (masked in API responses)
//	    "tls":   false                 // dial https if true
//	  }
//	}
//
// Other providers' runtime_config payloads pass through untouched — this
// decoder only reads keys that have meaning for the openclaw backend.
type openclawRuntimeConfig struct {
	Mode    string                       `json:"mode"`
	Gateway openclawRuntimeGatewayConfig `json:"gateway"`
}

// openclawRuntimeGatewayConfig is the owner-supplied Gateway endpoint.
//
// Trust boundary: in gateway mode the daemon writes this host:port into the
// per-task wrapper and the spawned openclaw CLI dials it. For self-hosted,
// single-tenant daemons this is the same trust level as custom_args /
// custom_env — the owner already controls the daemon host. Operators running
// a SHARED / managed daemon host should treat it as an SSRF surface (an agent
// owner could point the daemon at an arbitrary internal address) and
// gate/allowlist gateway targets accordingly.
type openclawRuntimeGatewayConfig struct {
	Host  string `json:"host"`
	Port  int    `json:"port"`
	Token string `json:"token"`
	TLS   bool   `json:"tls"`
}

// decodeOpenclawRuntimeConfig extracts the openclaw-specific knobs from an
// agent's runtime_config payload. Returns the routing mode plus the gateway
// pin shaped for execenv. The pin is non-zero only in gateway mode — any
// other mode drops it so a local-mode payload can't smuggle a bearer token
// into the per-task wrapper. Empty config is normalized to the current local default;
// malformed JSON and unknown modes are configuration errors, not permission to
// silently run the task through a different execution path.
func decodeOpenclawRuntimeConfig(raw json.RawMessage) (string, execenv.OpenclawGatewayPin, error) {
	if len(raw) == 0 {
		return "local", execenv.OpenclawGatewayPin{}, nil
	}
	var cfg openclawRuntimeConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return "", execenv.OpenclawGatewayPin{}, fmt.Errorf("decode openclaw runtime_config: %w", err)
	}
	if cfg.Mode == "" {
		cfg.Mode = "local"
	}
	if cfg.Mode != "local" && cfg.Mode != "gateway" {
		return "", execenv.OpenclawGatewayPin{}, fmt.Errorf("openclaw runtime_config mode %q is not local or gateway", cfg.Mode)
	}
	// Only gateway mode consults the pin. Local mode drops the gateway block so a stray
	// {"mode":"local","gateway":{...,"token":"..."}} never writes the bearer
	// token into the 0o600 per-task wrapper that `--local` makes openclaw ignore.
	if cfg.Mode != "gateway" {
		return cfg.Mode, execenv.OpenclawGatewayPin{}, nil
	}
	return cfg.Mode, execenv.OpenclawGatewayPin{
		Host:  cfg.Gateway.Host,
		Port:  cfg.Gateway.Port,
		Token: cfg.Gateway.Token,
		TLS:   cfg.Gateway.TLS,
	}, nil
}
