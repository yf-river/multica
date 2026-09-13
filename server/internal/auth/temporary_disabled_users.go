// 本文件实现「临时禁用用户」的应急黑名单：以硬编码的用户 ID/邮箱判定某些账户
// 暂时不可登录。这是一道临时的紧急开关——一旦账户封禁改为由 user 模型持久化
// 并强制执行，这里的名单就应该移除。
package auth

import (
	"errors"
	"strings"
)

// TemporarilyDisabledUserError 返回给被禁用户看的文案。
const TemporarilyDisabledUserError = "account disabled"

// ErrTemporarilyDisabledUser 程序化地表示「用户被临时禁用」。
var ErrTemporarilyDisabledUser = errors.New(TemporarilyDisabledUserError)

// 临时应急黑名单（按用户 ID）。一旦账户封禁持久化并纳入 user 模型，请移除本表。
var temporarilyDisabledUserIDs = map[string]struct{}{
	"514492f7-b30f-4147-bd33-c0e8ce5d6d4f": {},
	"1d542296-17c6-484a-9914-dcee589be116": {},
}

// 临时应急黑名单（按邮箱）。同上，持久化后应移除。
var temporarilyDisabledUserEmails = map[string]struct{}{
	"pdzzer68@embassybase.com": {},
	"gtwtrox@mowan666.com":     {},
}

// IsTemporarilyDisabledUser 判断该用户是否被临时禁用：命中 ID 名单或邮箱名单任一即 true。
func IsTemporarilyDisabledUser(userID, email string) bool {
	return IsTemporarilyDisabledUserID(userID) || IsTemporarilyDisabledUserEmail(email)
}

// IsTemporarilyDisabledUserID 判断 userID 是否在黑名单里。比较前转小写、去空白；
// 空值直接 false。
func IsTemporarilyDisabledUserID(userID string) bool {
	userID = strings.ToLower(strings.TrimSpace(userID))
	if userID == "" {
		return false
	}
	_, ok := temporarilyDisabledUserIDs[userID]
	return ok
}

// IsTemporarilyDisabledUserEmail 判断 email 是否在黑名单里。比较前转小写、去空白；
// 空值直接 false。
func IsTemporarilyDisabledUserEmail(email string) bool {
	email = strings.ToLower(strings.TrimSpace(email))
	if email == "" {
		return false
	}
	_, ok := temporarilyDisabledUserEmails[email]
	return ok
}
