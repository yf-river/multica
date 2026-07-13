package handler

import (
	"context"
	"strings"
	"testing"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

func TestResolveExternalCredentialTokenDoesNotFallBackAfterDecryptFailure(t *testing.T) {
	if testHandler == nil || testHandler.ExternalCredentialBox == nil {
		t.Skip("credential decryptor not available")
	}
	t.Setenv("CREDENTIAL_FALLBACK_MUST_NOT_BE_USED", "fallback-secret")
	profile := db.ExternalCredentialProfile{
		Provider:        externalCredentialProviderGongfeng,
		EncryptedSecret: []byte("corrupt ciphertext"),
		SecretRef:       "env:CREDENTIAL_FALLBACK_MUST_NOT_BE_USED",
	}

	token, err := testHandler.resolveExternalCredentialToken(profile)
	if err == nil || token != "" {
		t.Fatalf("token=%q err=%v, want explicit decrypt failure without env fallback", token, err)
	}
}

func TestSourceCredentialContextPreservesProfileReadFailure(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	h := *testHandler
	h.Queries = db.New(failNamedQueryDB{DBTX: testPool, queryName: "GetDefaultExternalCredentialProfileForUser"})

	_, err := h.sourceCredentialContext(context.Background(), parseUUID(testUserID), externalCredentialProviderGongfeng, gongfengMCPServerName)
	if err == nil || !strings.Contains(err.Error(), "load gongfeng credential profile") {
		t.Fatalf("source credential error = %v, want storage failure", err)
	}
}

func TestInjectSourceCredentialMCPEnvPreservesProfileReloadFailure(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	h := *testHandler
	h.Queries = db.New(failNamedQueryDB{DBTX: testPool, queryName: "GetDefaultExternalCredentialProfileForUser"})
	source := &protocol.TaskSourceContext{ExternalCredentials: map[string]protocol.TaskExternalCredentialContext{
		externalCredentialProviderGongfeng: {
			Provider:   externalCredentialProviderGongfeng,
			Configured: true,
			UserID:     testUserID,
			MCPServer:  gongfengMCPServerName,
		},
	}}

	if _, err := h.injectSourceCredentialMCPEnv(context.Background(), nil, source); err == nil || !strings.Contains(err.Error(), "reload gongfeng credential profile") {
		t.Fatalf("MCP injection error = %v, want profile reload failure", err)
	}
}
