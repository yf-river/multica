package main

import (
	"net"
	"os"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func TestMain(m *testing.M) {
	os.Unsetenv("MULTICA_AGENT_ID")
	os.Unsetenv("MULTICA_TASK_ID")
	os.Unsetenv("MULTICA_TOKEN")
	os.Exit(m.Run())
}

// testCmd returns a minimal cobra.Command with the --profile persistent flag
// registered, matching the rootCmd setup used in production.
func testCmd() *cobra.Command {
	cmd := &cobra.Command{}
	cmd.PersistentFlags().String("profile", "", "")
	return cmd
}

func TestResolveAppURL(t *testing.T) {
	cmd := testCmd()

	t.Run("prefers MULTICA_APP_URL", func(t *testing.T) {
		t.Setenv("MULTICA_APP_URL", "http://localhost:14000")
		t.Setenv("FRONTEND_ORIGIN", "http://localhost:13000")

		if got := resolveAppURL(cmd); got != "http://localhost:14000" {
			t.Fatalf("resolveAppURL() = %q, want %q", got, "http://localhost:14000")
		}
	})

	t.Run("falls back to FRONTEND_ORIGIN", func(t *testing.T) {
		t.Setenv("MULTICA_APP_URL", "")
		t.Setenv("FRONTEND_ORIGIN", "http://localhost:13026")

		if got := resolveAppURL(cmd); got != "http://localhost:13026" {
			t.Fatalf("resolveAppURL() = %q, want %q", got, "http://localhost:13026")
		}
	})
}

func TestResolveCallbackBinding(t *testing.T) {
	// Fake outbound detector: pretends the CLI has a fixed LAN IP regardless
	// of which server it dials.
	fixed := func(ip string) func(string) net.IP {
		return func(string) net.IP { return net.ParseIP(ip).To4() }
	}
	failing := func(string) net.IP { return nil }

	cases := []struct {
		name         string
		flagHost     string
		serverURL    string
		appURL       string
		detect       func(string) net.IP
		wantCallback string
		wantBind     string
	}{
		{
			name:         "public app URL stays on loopback",
			appURL:       "https://multica.ai",
			serverURL:    "https://api.multica.ai",
			detect:       failing,
			wantCallback: "localhost",
			wantBind:     "127.0.0.1",
		},
		{
			name:         "localhost app URL stays on loopback",
			appURL:       "http://localhost:3000",
			serverURL:    "http://localhost:8080",
			detect:       failing,
			wantCallback: "localhost",
			wantBind:     "127.0.0.1",
		},
		{
			name:         "same-machine self-host uses loopback (CLI IP matches app IP)",
			appURL:       "http://192.168.0.28:3000",
			serverURL:    "http://192.168.0.28:8080",
			detect:       fixed("192.168.0.28"),
			wantCallback: "localhost",
			wantBind:     "127.0.0.1",
		},
		{
			name:         "cross-machine self-host points callback at CLI's LAN IP",
			appURL:       "http://192.168.0.28:3000",
			serverURL:    "http://192.168.0.28:8080",
			detect:       fixed("192.168.0.47"),
			wantCallback: "192.168.0.47",
			wantBind:     "0.0.0.0",
		},
		{
			name:         "outbound detection failure falls back to app IP",
			appURL:       "http://192.168.0.28:3000",
			serverURL:    "http://192.168.0.28:8080",
			detect:       failing,
			wantCallback: "192.168.0.28",
			wantBind:     "0.0.0.0",
		},
		{
			name:         "--callback-host flag overrides everything",
			flagHost:     "cli.internal.example",
			appURL:       "https://multica.ai",
			serverURL:    "https://api.multica.ai",
			detect:       fixed("10.0.0.5"),
			wantCallback: "cli.internal.example",
			wantBind:     "0.0.0.0",
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			gotCallback, gotBind := resolveCallbackBinding(tc.flagHost, tc.serverURL, tc.appURL, tc.detect)
			if gotCallback != tc.wantCallback {
				t.Errorf("callback host = %q, want %q", gotCallback, tc.wantCallback)
			}
			if gotBind != tc.wantBind {
				t.Errorf("bind addr = %q, want %q", gotBind, tc.wantBind)
			}
		})
	}
}

// TestLoginTokenFlagWiring pins the current explicit-value contract. A bare
// --token is rejected by pflag rather than entering a second prompt flow.
func TestLoginTokenFlagWiring(t *testing.T) {
	tokenFlag := loginCmd.Flags().Lookup("token")
	if tokenFlag == nil {
		t.Fatal("loginCmd is missing the --token flag")
	}
	if got := tokenFlag.Value.Type(); got != "string" {
		t.Fatalf("loginCmd --token type = %q, want %q (regressed to bool?)", got, "string")
	}
	if tokenFlag.NoOptDefVal != "" {
		t.Fatalf("loginCmd --token unexpectedly accepts a missing value: NoOptDefVal=%q", tokenFlag.NoOptDefVal)
	}
}

// TestLoginTokenFlagParsing exercises the two current value forms and ensures
// the obsolete missing-value form cannot silently change meaning.
func TestLoginTokenFlagParsing(t *testing.T) {
	cases := []struct {
		name      string
		argv      []string
		wantToken string
		wantErr   bool
	}{
		{
			name:      "space-separated value",
			argv:      []string{"--token", "mul_xxx"},
			wantToken: "mul_xxx",
		},
		{
			name:      "equals-separated value",
			argv:      []string{"--token=mul_yyy"},
			wantToken: "mul_yyy",
		},
		{
			name:    "missing value is rejected",
			argv:    []string{"--token"},
			wantErr: true,
		},
		{
			name:      "explicit empty value stays empty",
			argv:      []string{"--token="},
			wantToken: "",
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			cmd := &cobra.Command{Use: "login"}
			cmd.Flags().String("token", "", "")
			err := cmd.ParseFlags(tc.argv)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("ParseFlags(%v) unexpectedly succeeded", tc.argv)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseFlags(%v) error: %v", tc.argv, err)
			}
			tokenFlag, _ := cmd.Flags().GetString("token")
			if tokenFlag != tc.wantToken {
				t.Fatalf("resolved token = %q, want %q", tokenFlag, tc.wantToken)
			}
		})
	}
}

func TestNormalizeAPIBaseURL(t *testing.T) {
	t.Run("converts websocket base URL", func(t *testing.T) {
		if got := normalizeAPIBaseURL("ws://localhost:18106/ws"); got != "http://localhost:18106" {
			t.Fatalf("normalizeAPIBaseURL() = %q, want %q", got, "http://localhost:18106")
		}
	})

	t.Run("keeps http base URL", func(t *testing.T) {
		if got := normalizeAPIBaseURL("http://localhost:8080"); got != "http://localhost:8080" {
			t.Fatalf("normalizeAPIBaseURL() = %q, want %q", got, "http://localhost:8080")
		}
	})

	t.Run("falls back to raw value for invalid URL", func(t *testing.T) {
		if got := normalizeAPIBaseURL("://bad-url"); got != "://bad-url" {
			t.Fatalf("normalizeAPIBaseURL() = %q, want %q", got, "://bad-url")
		}
	})
}

// TestValidateLoginTokenPrefix pins the accepted PAT prefix set for
// `multica login --token`. The original implementation hardcoded `mul_`
// only, which rejected legitimate Multica Cloud Node PATs (`mcn_`) at
// the CLI even though the server's middleware would have accepted them.
// If a future change drops `mcn_` from the list (or accidentally
// broadens the set to anything-goes), this test fails.
func TestValidateLoginTokenPrefix(t *testing.T) {
	cases := []struct {
		name    string
		token   string
		wantErr bool
	}{
		{name: "mul_ PAT", token: "mul_abc123", wantErr: false},
		{name: "mcn_ Cloud Node PAT", token: "mcn_abc123", wantErr: false},
		{name: "empty token", token: "", wantErr: true},
		{name: "no prefix", token: "abc123", wantErr: true},
		{name: "wrong prefix mdt_", token: "mdt_abc123", wantErr: true},
		{name: "wrong prefix mat_", token: "mat_abc123", wantErr: true},
		{name: "case-sensitive: MUL_ rejected", token: "MUL_abc123", wantErr: true},
		{name: "leading whitespace not allowed (callers TrimSpace first)", token: " mul_abc", wantErr: true},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			err := validateLoginTokenPrefix(tc.token)
			if tc.wantErr && err == nil {
				t.Fatalf("validateLoginTokenPrefix(%q) = nil, want error", tc.token)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("validateLoginTokenPrefix(%q) = %v, want nil", tc.token, err)
			}
		})
	}

	// The error string is user-facing; make sure it lists every accepted
	// prefix so users hitting it can self-serve. Hardcoding the exact
	// prefixes here is deliberate — if someone adds a new prefix to
	// loginTokenPrefixes they should also update the docs / this test.
	err := validateLoginTokenPrefix("nope_xxx")
	if err == nil {
		t.Fatal("expected error for unknown prefix")
	}
	for _, p := range []string{"mul_", "mcn_"} {
		if !strings.Contains(err.Error(), p) {
			t.Errorf("error %q does not mention prefix %q", err.Error(), p)
		}
	}
}
