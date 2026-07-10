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

func TestNewRouterWithOptionsRejectsMalformedConfiguredSecret(t *testing.T) {
	clearStartupConfig(t)
	t.Setenv("MULTICA_EXTERNAL_CREDENTIAL_KEY", "not-valid-base64")

	_, _, err := NewRouterWithOptions(nil, realtime.NewHub(), events.New(), analytics.NoopClient{}, nil, RouterOptions{})
	if err == nil || !strings.Contains(err.Error(), "external credential encryption") {
		t.Fatalf("NewRouterWithOptions error = %v, want external credential configuration error", err)
	}
}

func TestNewRouterWithOptionsRejectsMalformedLarkSecret(t *testing.T) {
	clearStartupConfig(t)
	t.Setenv("MULTICA_LARK_SECRET_KEY", "not-valid-base64")

	_, _, err := NewRouterWithOptions(nil, realtime.NewHub(), events.New(), analytics.NoopClient{}, nil, RouterOptions{})
	if err == nil || !strings.Contains(err.Error(), "Lark credential encryption") {
		t.Fatalf("NewRouterWithOptions error = %v, want Lark credential configuration error", err)
	}
}

func TestValidateAttachmentDeliveryRejectsImpossibleModes(t *testing.T) {
	clearStartupConfig(t)
	local := storage.NewLocalStorageFromEnv()
	if local == nil {
		t.Fatal("local storage setup failed")
	}

	if err := validateAttachmentDelivery(local, nil, "presign"); err == nil || !strings.Contains(err.Error(), "S3-compatible") {
		t.Fatalf("presign validation error = %v", err)
	}
	if err := validateAttachmentDelivery(local, nil, "cloudfront"); err == nil || !strings.Contains(err.Error(), "requires complete") {
		t.Fatalf("cloudfront validation error = %v", err)
	}
	if err := validateAttachmentDelivery(local, nil, "mystery"); err == nil || !strings.Contains(err.Error(), "must be") {
		t.Fatalf("unknown mode validation error = %v", err)
	}
	if err := validateAttachmentDelivery(local, &auth.CloudFrontSigner{}, "auto"); err == nil || !strings.Contains(err.Error(), "requires S3_BUCKET") {
		t.Fatalf("local CloudFront signer validation error = %v", err)
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
