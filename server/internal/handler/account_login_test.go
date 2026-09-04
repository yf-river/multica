package handler

import "testing"

func TestAccountPasswordLoginValidAcceptsExistingLegacyPassword(t *testing.T) {
	if !accountPasswordLoginValid("develop123") {
		t.Fatal("已有账号应能继续使用符合长度要求的历史密码")
	}
	if accountPasswordValid("develop123") {
		t.Fatal("历史密码不应被当成符合新账号强度要求的密码")
	}
}

func TestAccountPasswordLoginValidRejectsInvalidLength(t *testing.T) {
	for _, password := range []string{"short", "123456789012345678901234567890123"} {
		if accountPasswordLoginValid(password) {
			t.Fatalf("登录密码长度校验不应接受 %d 个字符", len(password))
		}
	}
}
