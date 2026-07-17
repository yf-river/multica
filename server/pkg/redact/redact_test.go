package redact

import (
	"strings"
	"testing"
)

func TestRedactSensitiveValues(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name, input, secret, marker string
	}{
		{"AWS access key", "Found key AKIAIOSFODNN7EXAMPLE in config", "AKIAIOSFODNN7EXAMPLE", "[REDACTED AWS KEY]"},
		{"AWS secret key", "aws_secret_access_key = wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY", "wJalrXUtnFEMI", ""},
		{"private key", "Here is the key:\n-----BEGIN RSA PRIVATE KEY-----\nMIIEow...\n-----END RSA PRIVATE KEY-----\nDone.", "MIIEow", "[REDACTED PRIVATE KEY]"},
		{"GitHub token", "export GITHUB_TOKEN=ghp_ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmn", "ghp_", ""},
		{"GitLab token", "GITLAB_TOKEN=glpat-AbCdEfGhIjKlMnOpQrStUvWx", "glpat-", ""},
		{"OpenAI key", "OPENAI_API_KEY=sk-proj-abc123def456ghi789jkl012mno345", "sk-proj-abc123", ""},
		{"Slack token", "token: xoxb-123456789012-1234567890123-AbCdEfGhIjKl", "xoxb-", ""},
		{"bearer token", "Authorization: Bearer eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.abc123", "eyJhbGci", ""},
		{"JWT", "token: eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.SflKxwRJSMeKKF2QT4fwpMeJf36POk6yJV_adQssw5c", "eyJhbGci", ""},
		{"connection string", "connecting to postgres://admin:s3cret@db.example.com:5432/mydb", "s3cret", ""},
		{"API key variable", "API_KEY=mysupersecretkey123", "mysupersecretkey123", "[REDACTED CREDENTIAL]"},
		{"database URL variable", "DATABASE_URL=postgres://user:pass@host/db", "postgres://user:pass", "[REDACTED CREDENTIAL]"},
		{"database password variable", "DB_PASSWORD: hunter2", "hunter2", "[REDACTED CREDENTIAL]"},
		{"password variable", "PASSWORD=hunter2", "hunter2", "[REDACTED CREDENTIAL]"},
		{"secret variable", "SECRET=mysecretvalue", "mysecretvalue", "[REDACTED CREDENTIAL]"},
		{"token variable", "TOKEN=abc123xyz", "abc123xyz", "[REDACTED CREDENTIAL]"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Text(tc.input)
			if strings.Contains(got, tc.secret) {
				t.Fatalf("secret %q was not redacted: %s", tc.secret, got)
			}
			if tc.marker != "" && !strings.Contains(got, tc.marker) {
				t.Fatalf("expected marker %q, got: %s", tc.marker, got)
			}
		})
	}
}

func TestRedactHomeDirectory(t *testing.T) {
	t.Parallel()
	if homeDir == "" || username == "" {
		t.Skip("cannot determine home dir or username")
	}
	input := "Reading file at " + homeDir + "/Documents/secret.txt"
	got := Text(input)
	if strings.Contains(got, username) {
		t.Fatalf("home directory username not redacted: %s", got)
	}
	if !strings.Contains(got, "****") {
		t.Fatalf("expected **** in path, got: %s", got)
	}
}

func TestNoFalsePositivesOnNormalText(t *testing.T) {
	t.Parallel()
	inputs := []string{
		"This is a normal commit message about fixing a bug",
		"The function returns skip-navigation as the class name",
		"Created PR #42 for the authentication feature",
		"Running tests in /tmp/test-workspace/project",
		"The API endpoint /api/issues/123 was updated",
	}
	for _, input := range inputs {
		got := Text(input)
		if got != input {
			t.Fatalf("false positive redaction:\n  input:  %s\n  output: %s", input, got)
		}
	}
}

func TestRedactMultipleSecrets(t *testing.T) {
	t.Parallel()
	input := "Keys: AKIAIOSFODNN7EXAMPLE and ghp_ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmn"
	got := Text(input)
	if strings.Contains(got, "AKIAIOSFODNN7EXAMPLE") {
		t.Fatal("AWS key not redacted in multi-secret text")
	}
	if strings.Contains(got, "ghp_") {
		t.Fatal("GitHub token not redacted in multi-secret text")
	}
}
