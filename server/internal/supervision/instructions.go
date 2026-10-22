package supervision

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"strings"
)

// DefaultInstructions 是工作区未自定义时使用的监督提示词。放在代码里，平台修
// 正审查标准或结果要求后，没有自定义过的工作区会跟随升级。用户一旦保存自己的
// 提示词，就固定使用该工作区的内容。
//
//go:embed default_instructions.md
var DefaultInstructions string

// InstructionsVersion 用于在设置页展示默认提示词的版本，并在审查历史中区分
// 同一工作区不同时期的指令。内容变化即版本变化，不需要手工维护编号。
var InstructionsVersion = instructionsVersion(DefaultInstructions)

func instructionsVersion(text string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(text)))
	return "default-" + hex.EncodeToString(sum[:4])
}

// EffectiveInstructions 返回本次审查实际使用的提示词：自定义优先，空值跟随默认。
func EffectiveInstructions(custom string) string {
	if strings.TrimSpace(custom) != "" {
		return custom
	}
	return DefaultInstructions
}
