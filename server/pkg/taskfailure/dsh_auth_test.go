package taskfailure

import "testing"

// 原生 DSH 在请求前中止时使用稳定的认证错误码。
func TestClassifyDshAccountAuthentication(t *testing.T) {
	for _, message := range []string{
		"ACCOUNT_SIGN_IN_REQUIRED: Sign in to DeepSeek to use the account provider.",
		"DSH_TURN_ABORTED: DSH turn was aborted: deepseek-account/signed-out",
	} {
		if got := Classify(message); got != ReasonAgentProviderAuthOrAccess {
			t.Fatalf("%q classified as %s", message, got)
		}
	}
}
