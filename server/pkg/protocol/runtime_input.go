package protocol

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

type TaskRuntimeInput struct {
	ID            string `json:"id"`
	TaskID        string `json:"task_id"`
	AuthorID      string `json:"author_id"`
	Kind          string `json:"kind"`
	Content       string `json:"content"`
	Status        string `json:"status"`
	Output        string `json:"output"`
	FailureReason string `json:"failure_reason"`
	CreatedAt     string `json:"created_at"`
}

type RuntimeInputClaim struct {
	RuntimeID    string `json:"runtime_id"`
	DispatchedAt string `json:"dispatched_at"`
}

type RuntimeInputAck struct {
	RuntimeInputClaim
	Status        string `json:"status"`
	Output        string `json:"output"`
	FailureReason string `json:"failure_reason"`
}

func ValidateRuntimeInput(kind, content string) error {
	if strings.TrimSpace(content) == "" || len(content) > 32000 || strings.ContainsRune(content, '\x00') {
		return fmt.Errorf("input is empty or exceeds 32000 bytes")
	}
	if kind == "text" {
		return nil
	}
	if kind != "command" {
		return fmt.Errorf("unsupported runtime input kind")
	}
	command := strings.TrimSpace(content)
	if strings.HasPrefix(command, "/goal ") && utf8.RuneCountInString(strings.TrimSpace(strings.TrimPrefix(command, "/goal"))) > 4000 {
		return fmt.Errorf("goal objective must be at most 4000 characters")
	}
	if command == "/plan" || command == "/plan off" || command == "/goal" || strings.HasPrefix(command, "/goal ") {
		return nil
	}
	return fmt.Errorf("unsupported native runtime command")
}
