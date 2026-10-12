package handler

import "testing"

func TestAccountPasswordLoginValidAcceptsExistingLegacyPassword(t *testing.T) {
	for _, password := range []string{"auth", "develop123"} {
		if !accountPasswordLoginValid(password) {
			t.Fatal("已有账号应能使用数据库中存储的密码登录")
		}
		if accountPasswordValid(password) {
			t.Fatal("已有弱密码不应满足新账号的密码强度要求")
		}
	}
}

func TestAccountPasswordLoginValidRejectsInvalidLength(t *testing.T) {
	for _, password := range []string{"", "123456789012345678901234567890123"} {
		if accountPasswordLoginValid(password) {
			t.Fatalf("登录密码长度校验不应接受 %d 个字符", len(password))
		}
	}
}
