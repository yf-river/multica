package engine

import (
	"strings"

	"github.com/multica-ai/multica/server/internal/chattitle"
	"github.com/multica-ai/multica/server/internal/integrations/channel"
)

// 角色定位：渠道创建的 Chat（会话）如何生成标题的共享辅助。标题推导本身在
// chattitle.Derive；本文件只是把"首条消息有媒体占位时先去占位再推导""纯媒体首条消息按
// 媒体类型给固定标题"这些渠道特有细节收口到这里。

// DeriveChatTitle 是所有渠道创建 Chat 共享的确定性兜底标题推导。按 Unicode 码点计数，
// 含结尾省略号。确定性 = 同一输入永远同一标题，方便对消息做稳定的标题聚合。
func DeriveChatTitle(body string) string {
	return chattitle.Derive(body)
}

// deriveFirstMessageTitle 用**首条消息**推导会话标题。首条是媒体时先去掉媒体占位行
// （占位符文本没语义，混进去会把标题带偏）。
func deriveFirstMessageTitle(body string, hasMedia bool) string {
	if hasMedia {
		body = withoutMediaPlaceholderLines(body)
	}
	return DeriveChatTitle(body)
}

// withoutMediaPlaceholderLines 去掉正文里的整行媒体占位符（[Image]/[File]/[Audio]/
// [Video]）。逐行匹配而非子串替换，避免误删正文中偶然出现的同样文本。
func withoutMediaPlaceholderLines(body string) string {
	lines := make([]string, 0, strings.Count(body, "\n")+1)
	for _, line := range strings.Split(body, "\n") {
		switch strings.TrimSpace(line) {
		case "[Image]", "[File]", "[Audio]", "[Video]":
			continue
		default:
			lines = append(lines, line)
		}
	}
	return strings.Join(lines, "\n")
}

// mediaTypeTitle 给"首条就是纯媒体消息"的会话一个固定标题（图片/音频/视频/文件聊天）。
func mediaTypeTitle(kind channel.MsgType) string {
	switch kind {
	case channel.MsgTypeImage:
		return "Image chat"
	case channel.MsgTypeAudio:
		return "Audio chat"
	case channel.MsgTypeVideo:
		return "Video chat"
	default:
		return "File chat"
	}
}

// chatTitleSource 用发送者自己的文字生成标题，消费过的清空指令不进入标题。
func chatTitleSource(body, commandText string, consumedFresh bool) string {
	if strings.TrimSpace(commandText) != "" {
		if consumedFresh {
			if current, ok := ParseFreshSessionCommand(commandText); ok {
				return current
			}
		}
		return commandText
	}
	return body
}
