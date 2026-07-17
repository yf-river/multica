package daemon

import (
	"encoding/json"
	"testing"

	"github.com/multica-ai/multica/server/internal/daemon/execenv"
)

func TestDecodeOpenclawRuntimeConfig(t *testing.T) {
	gateway := execenv.OpenclawGatewayPin{Host: "gw.internal", Port: 18789, Token: "secret", TLS: true}
	for _, tc := range []struct {
		name, raw, wantMode string
		wantGateway         execenv.OpenclawGatewayPin
		wantErr             bool
	}{
		{"empty defaults local", "", "local", execenv.OpenclawGatewayPin{}, false},
		{"gateway mode", `{"mode":"gateway","gateway":{"host":"gw.internal","port":18789,"token":"secret","tls":true}}`, "gateway", gateway, false},
		{"malformed payload", `{"mode":"gateway"`, "", execenv.OpenclawGatewayPin{}, true},
		{"gateway mode without pin", `{"mode":"gateway"}`, "gateway", execenv.OpenclawGatewayPin{}, false},
		{"local mode drops gateway pin", `{"mode":"local","gateway":{"host":"gw.internal","port":18789,"token":"secret","tls":true}}`, "local", execenv.OpenclawGatewayPin{}, false},
		{"unknown mode", `{"mode":"gatway","gateway":{"host":"gw.internal","port":18789,"token":"secret"}}`, "", execenv.OpenclawGatewayPin{}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			mode, gotGateway, err := decodeOpenclawRuntimeConfig(json.RawMessage(tc.raw))
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected decode error")
				}
				return
			}
			if err != nil {
				t.Fatalf("decode: %v", err)
			}
			if mode != tc.wantMode || gotGateway != tc.wantGateway {
				t.Fatalf("mode/gateway = %q/%+v, want %q/%+v", mode, gotGateway, tc.wantMode, tc.wantGateway)
			}
		})
	}
}
