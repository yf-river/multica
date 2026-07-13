package daemon

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/daemon/execenv"
)

func TestDecodeOpenclawRuntimeConfigEmpty(t *testing.T) {
	t.Parallel()

	mode, gw, err := decodeOpenclawRuntimeConfig(nil)
	if err != nil {
		t.Fatalf("decode empty config: %v", err)
	}
	if mode != "" {
		t.Errorf("mode for nil payload: got %q, want \"\"", mode)
	}
	if !gw.IsZero() {
		t.Errorf("gateway for nil payload: got %+v, want zero", gw)
	}
}

func TestDecodeOpenclawRuntimeConfigGatewayMode(t *testing.T) {
	t.Parallel()

	raw := json.RawMessage(`{
		"mode": "gateway",
		"gateway": {
			"host": "gw.internal",
			"port": 18789,
			"token": "secret",
			"tls": true
		}
	}`)
	mode, gw, err := decodeOpenclawRuntimeConfig(raw)
	if err != nil {
		t.Fatalf("decode gateway config: %v", err)
	}
	if mode != "gateway" {
		t.Errorf("mode: got %q, want %q", mode, "gateway")
	}
	want := execenv.OpenclawGatewayPin{
		Host:  "gw.internal",
		Port:  18789,
		Token: "secret",
		TLS:   true,
	}
	if gw != want {
		t.Errorf("gateway: got %+v, want %+v", gw, want)
	}
}

func TestDecodeOpenclawRuntimeConfigRejectsMalformedPayload(t *testing.T) {
	t.Parallel()

	if _, _, err := decodeOpenclawRuntimeConfig(json.RawMessage(`{"mode": "gateway"`)); err == nil {
		t.Fatal("malformed payload should fail")
	}
}

func TestDecodeOpenclawRuntimeConfigModeOnly(t *testing.T) {
	t.Parallel()

	// Users may switch to gateway mode and rely on the daemon host's local
	// ~/.openclaw/openclaw.json for the endpoint — gateway block stays zero.
	mode, gw, err := decodeOpenclawRuntimeConfig(json.RawMessage(`{"mode": "gateway"}`))
	if err != nil {
		t.Fatalf("decode mode-only gateway config: %v", err)
	}
	if mode != "gateway" {
		t.Errorf("mode: got %q, want %q", mode, "gateway")
	}
	if !gw.IsZero() {
		t.Errorf("gateway: got %+v, want zero", gw)
	}
}

// TestOpenclawGatewayPinDefaultFormattingMasksToken — a stray `%v` /
// `%+v` / json.Marshal of an OpenclawGatewayPin must NOT print the bearer
// token verbatim. The wrapper-config writer still gets the real value
// directly off the Token field; only default formatters get redacted.
// Guards against the secondary leak path called out in the issue #3260 CR.
func TestOpenclawGatewayPinDefaultFormattingMasksToken(t *testing.T) {
	t.Parallel()

	pin := execenv.OpenclawGatewayPin{
		Host:  "gw.internal",
		Port:  18789,
		Token: "real-secret",
		TLS:   true,
	}

	if got := pin.String(); strings.Contains(got, "real-secret") {
		t.Errorf("String() leaks token: %q", got)
	}
	if got := fmt.Sprintf("%+v", pin); strings.Contains(got, "real-secret") {
		t.Errorf("%%+v leaks token: %q", got)
	}
	raw, err := json.Marshal(pin)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(raw), "real-secret") {
		t.Errorf("MarshalJSON leaks token: %s", raw)
	}
	// Sanity: the host stays visible so the masked payload is still
	// useful for debugging the non-secret half of the pin.
	if !strings.Contains(string(raw), "gw.internal") {
		t.Errorf("MarshalJSON dropped host along with token: %s", raw)
	}
}

// TestDecodeOpenclawRuntimeConfigLocalModeDropsGatewayPin — a local-mode
// payload that still carries a gateway block (craftable via a direct PATCH)
// must not surface the pin. Otherwise the bearer token would be written into
// the 0o600 per-task wrapper that `--local` makes openclaw ignore.
func TestDecodeOpenclawRuntimeConfigLocalModeDropsGatewayPin(t *testing.T) {
	t.Parallel()

	raw := json.RawMessage(`{
		"mode": "local",
		"gateway": {"host": "gw.internal", "port": 18789, "token": "secret", "tls": true}
	}`)
	mode, gw, err := decodeOpenclawRuntimeConfig(raw)
	if err != nil {
		t.Fatalf("decode local config: %v", err)
	}
	if mode != "local" {
		t.Errorf("mode: got %q, want %q", mode, "local")
	}
	if !gw.IsZero() {
		t.Errorf("gateway for local mode: got %+v, want zero", gw)
	}
}

func TestDecodeOpenclawRuntimeConfigRejectsUnknownMode(t *testing.T) {
	t.Parallel()

	raw := json.RawMessage(`{
		"mode": "gatway",
		"gateway": {"host": "gw.internal", "port": 18789, "token": "secret"}
	}`)
	if _, _, err := decodeOpenclawRuntimeConfig(raw); err == nil {
		t.Fatal("unknown mode should fail")
	}
}
