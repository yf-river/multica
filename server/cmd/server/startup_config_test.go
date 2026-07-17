package main

import (
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/analytics"
	"github.com/multica-ai/multica/server/internal/auth"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/realtime"
	"github.com/multica-ai/multica/server/internal/storage"
)

func clearStartupConfig(t *testing.T) {
	t.Helper()
	for _, key := range []string{
		"S3_BUCKET", "S3_REGION", "AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY",
		"AWS_ENDPOINT_URL", "CLOUDFRONT_DOMAIN", "CLOUDFRONT_KEY_PAIR_ID",
		"CLOUDFRONT_PRIVATE_KEY_SECRET", "CLOUDFRONT_PRIVATE_KEY", "COOKIE_DOMAIN",
		"ATTACHMENT_DOWNLOAD_MODE", "MULTICA_EXTERNAL_CREDENTIAL_KEY",
		"MULTICA_LARK_SECRET_KEY", "MULTICA_LARK_HTTP_BASE_URL",
		"MULTICA_LARK_CALLBACK_BASE_URL", "MULTICA_LARK_REGISTRATION_DOMAIN",
		"MULTICA_LARK_REGISTRATION_LARK_DOMAIN",
	} {
		t.Setenv(key, "")
	}
	t.Setenv("LOCAL_UPLOAD_DIR", t.TempDir())
}

func TestNewRouterWithOptionsRejectsMalformedSecrets(t *testing.T) {
	for _, tt := range []struct {
		envKey  string
		wantErr string
	}{
		{envKey: "MULTICA_EXTERNAL_CREDENTIAL_KEY", wantErr: "external credential encryption"},
		{envKey: "MULTICA_LARK_SECRET_KEY", wantErr: "Lark credential encryption"},
	} {
		t.Run(tt.envKey, func(t *testing.T) {
			clearStartupConfig(t)
			t.Setenv(tt.envKey, "not-valid-base64")

			_, _, err := NewRouterWithOptions(nil, realtime.NewHub(), events.New(), analytics.NoopClient{}, nil, RouterOptions{})
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("NewRouterWithOptions error = %v, want %q", err, tt.wantErr)
			}
		})
	}
}

func TestNewRouterWithOptionsRequiresHeartbeatScheduler(t *testing.T) {
	clearStartupConfig(t)

	_, _, err := NewRouterWithOptions(nil, realtime.NewHub(), events.New(), analytics.NoopClient{}, nil, RouterOptions{})
	if err == nil || !strings.Contains(err.Error(), "heartbeat scheduler is required") {
		t.Fatalf("NewRouterWithOptions error = %v, want missing heartbeat scheduler error", err)
	}
}

func TestValidateAttachmentDeliveryRejectsImpossibleModes(t *testing.T) {
	clearStartupConfig(t)
	local := storage.NewLocalStorageFromEnv()
	if local == nil {
		t.Fatal("local storage setup failed")
	}

	for _, tt := range []struct {
		mode    string
		signer  *auth.CloudFrontSigner
		wantErr string
	}{
		{mode: "presign", wantErr: "S3-compatible"},
		{mode: "cloudfront", wantErr: "requires complete"},
		{mode: "mystery", wantErr: "must be"},
		{mode: "auto", signer: &auth.CloudFrontSigner{}, wantErr: "requires S3_BUCKET"},
	} {
		t.Run(tt.mode, func(t *testing.T) {
			if err := validateAttachmentDelivery(local, tt.signer, tt.mode); err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("validation error = %v, want %q", err, tt.wantErr)
			}
		})
	}
}

func TestValidateOptionalHTTPBaseURL(t *testing.T) {
	if err := validateOptionalHTTPBaseURL("TEST_URL", "https://lark.example.test/base"); err != nil {
		t.Fatalf("valid URL rejected: %v", err)
	}
	if err := validateOptionalHTTPBaseURL("TEST_URL", "not-a-url"); err == nil || !strings.Contains(err.Error(), "TEST_URL") {
		t.Fatalf("invalid URL error = %v", err)
	}
}
