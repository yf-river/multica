// 本文件提供 agent 头像的默认处理：新建 agent 未显式指定头像时，随机分配一个
// emoji 头像（emoji: 前缀）；显式 URL 则走与更新路径相同的 acceptAvatarURL 鉴权
// 边界（创建路径同样是一种发布存储对象的方式）。
package handler

import (
	"crypto/rand"
	"math/big"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5/pgtype"
)

// agentEmojiAvatarPrefix 是 emoji 头像值的线格式前缀，用于与外部图片 URL 区分。
const agentEmojiAvatarPrefix = "emoji:"

// agentEmojiAvatars 是可供随机分配的默认 emoji 头像池。
var agentEmojiAvatars = []string{
	"🐙", "🦊", "🦉", "🐝", "🐼", "🐸", "🐯", "🦁",
	"🐨", "🐵", "🐧", "🐳", "🦋", "🌞", "🌙", "⭐",
	"🔥", "⚡", "🍀", "🌈", "🚀", "🤖", "👾", "🧠",
}

// randomAgentEmojiAvatar 从池中均匀随机挑一个 emoji 头像；随机源出错时退回池中
// 第一个，保证任何情况下都有确定值可落库。
func randomAgentEmojiAvatar() string {
	index, err := rand.Int(rand.Reader, big.NewInt(int64(len(agentEmojiAvatars))))
	if err != nil {
		return agentEmojiAvatarPrefix + agentEmojiAvatars[0]
	}
	return agentEmojiAvatarPrefix + agentEmojiAvatars[index.Int64()]
}

// newAgentAvatar 解析要为新 agent 持久化的头像。显式值经 acceptAvatarURL 校验——
// 创建路径仍是发布存储对象的一种方式，因此携带与更新相同的授权边界。ok=false
// 表示错误响应已写好，调用方必须中止。
func (h *Handler) newAgentAvatar(w http.ResponseWriter, r *http.Request, avatarURL *string) (pgtype.Text, bool) {
	if avatarURL != nil && strings.TrimSpace(*avatarURL) != "" {
		accepted, ok := h.acceptAvatarURL(w, r, *avatarURL, "")
		if !ok {
			return pgtype.Text{}, false
		}
		return pgtype.Text{String: accepted, Valid: true}, true
	}
	return pgtype.Text{String: randomAgentEmojiAvatar(), Valid: true}, true
}
