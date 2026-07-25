package daemon

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/daemon/execenv"
)

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestDecodeOpenclawRuntimeConfigEmpty(t *testing.T) {
	t.Parallel()

	mode, gw := decodeOpenclawRuntimeConfig(nil, quietLogger())
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
	mode, gw := decodeOpenclawRuntimeConfig(raw, quietLogger())
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

func TestDecodeOpenclawRuntimeConfigMalformedFailsSoftToLocal(t *testing.T) {
	t.Parallel()

	// A broken JSON blob must never block dispatch — the agent runs in the
	// historical embedded mode until the user fixes the config.
	mode, gw := decodeOpenclawRuntimeConfig(json.RawMessage(`{"mode": "gateway"`), quietLogger())
	if mode != "" {
		t.Errorf("mode for malformed payload: got %q, want \"\"", mode)
	}
	if !gw.IsZero() {
		t.Errorf("gateway for malformed payload: got %+v, want zero", gw)
	}
}

func TestDecodeOpenclawRuntimeConfigModeOnly(t *testing.T) {
	t.Parallel()

	// Users may switch to gateway mode and rely on the daemon host's local
	// ~/.openclaw/openclaw.json for the endpoint — gateway block stays zero.
	mode, gw := decodeOpenclawRuntimeConfig(json.RawMessage(`{"mode": "gateway"}`), quietLogger())
	if mode != "gateway" {
		t.Errorf("mode: got %q, want %q", mode, "gateway")
	}
	if !gw.IsZero() {
		t.Errorf("gateway: got %+v, want zero", gw)
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
	mode, gw := decodeOpenclawRuntimeConfig(raw, quietLogger())
	if mode != "local" {
		t.Errorf("mode: got %q, want %q", mode, "local")
	}
	if !gw.IsZero() {
		t.Errorf("gateway for local mode: got %+v, want zero", gw)
	}
}

// TestDecodeOpenclawRuntimeConfigUnknownModeWarnsAndDropsPin — a typo'd mode
// neither behaves like gateway nor silently like local: it falls back to local
// (zero pin) AND logs a WARN so the misconfiguration is discoverable.
func TestDecodeOpenclawRuntimeConfigUnknownModeWarnsAndDropsPin(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn}))
	raw := json.RawMessage(`{
		"mode": "gatway",
		"gateway": {"host": "gw.internal", "port": 18789, "token": "secret"}
	}`)
	mode, gw := decodeOpenclawRuntimeConfig(raw, logger)
	if mode != "gatway" {
		t.Errorf("mode: got %q, want %q", mode, "gatway")
	}
	if !gw.IsZero() {
		t.Errorf("gateway for unknown mode: got %+v, want zero", gw)
	}
	if !strings.Contains(buf.String(), "unrecognized mode") {
		t.Errorf("expected WARN about unrecognized mode, got: %q", buf.String())
	}
}
