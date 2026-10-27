package protocol

import (
	"fmt"
	"strings"
)

type InteractionOption struct {
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
}

type InteractionQuestion struct {
	ID          string              `json:"id"`
	Header      string              `json:"header,omitempty"`
	Detail      string              `json:"detail,omitempty"`
	Question    string              `json:"question"`
	Options     []InteractionOption `json:"options,omitempty"`
	MultiSelect bool                `json:"multi_select,omitempty"`
}

type InteractionPrompt struct {
	Kind         string                `json:"kind"`
	Title        string                `json:"title"`
	PlanMarkdown string                `json:"plan_markdown,omitempty"`
	Questions    []InteractionQuestion `json:"questions,omitempty"`
}

type InteractionAnswer struct {
	Decision string              `json:"decision"`
	Feedback string              `json:"feedback,omitempty"`
	Answers  map[string][]string `json:"answers,omitempty"`
}

type TaskInteraction struct {
	ID             string             `json:"id"`
	TaskID         string             `json:"task_id"`
	Provider       string             `json:"provider"`
	Prompt         InteractionPrompt  `json:"prompt"`
	Status         string             `json:"status"`
	DeliveryMode   string             `json:"delivery_mode"`
	FollowupTaskID string             `json:"followup_task_id,omitempty"`
	Response       *InteractionAnswer `json:"response,omitempty"`
	CreatedAt      string             `json:"created_at"`
	ExpiresAt      string             `json:"expires_at"`
}

func (p InteractionPrompt) Validate() error {
	if p.Kind != "question" && p.Kind != "approval" && p.Kind != "plan" {
		return fmt.Errorf("unsupported interaction kind")
	}
	if strings.TrimSpace(p.Title) == "" || len(p.Title) > 32768 || strings.ContainsRune(p.Title, '\x00') {
		return fmt.Errorf("interaction title is required and must fit 32768 bytes")
	}
	if len(p.PlanMarkdown) > 256*1024 || strings.ContainsRune(p.PlanMarkdown, '\x00') {
		return fmt.Errorf("invalid plan Markdown")
	}
	if p.Kind != "plan" && p.PlanMarkdown != "" {
		return fmt.Errorf("only plans may contain plan Markdown")
	}
	if len(p.Questions) > 10 || (p.Kind == "question" && len(p.Questions) == 0) {
		return fmt.Errorf("invalid interaction question count")
	}
	seen := map[string]bool{}
	for _, q := range p.Questions {
		if q.ID == "" || len(q.ID) > 128 || strings.ContainsRune(q.ID, '\x00') || seen[q.ID] || strings.TrimSpace(q.Question) == "" || len(q.Question) > 16384 || len(q.Header) > 1024 || len(q.Detail) > 65536 || len(q.Options) > 30 || strings.ContainsRune(q.Question, '\x00') || strings.ContainsRune(q.Header, '\x00') || strings.ContainsRune(q.Detail, '\x00') {
			return fmt.Errorf("invalid interaction question")
		}
		seen[q.ID] = true
		for _, option := range q.Options {
			if strings.TrimSpace(option.Label) == "" || len(option.Label) > 1024 || len(option.Description) > 4096 || strings.ContainsRune(option.Label, '\x00') || strings.ContainsRune(option.Description, '\x00') {
				return fmt.Errorf("invalid interaction option")
			}
		}
	}
	return nil
}

func (p InteractionPrompt) ValidateAnswer(a InteractionAnswer) error {
	if a.Feedback != "" {
		return fmt.Errorf("live interactions cannot contain continuation feedback")
	}
	if a.Decision != "allow" && a.Decision != "deny" {
		return fmt.Errorf("interaction decision must be allow or deny")
	}
	if a.Decision == "deny" {
		if len(a.Answers) != 0 {
			return fmt.Errorf("declined interaction cannot contain answers")
		}
		return nil
	}
	if p.Kind != "question" {
		if len(a.Answers) != 0 {
			return fmt.Errorf("approval cannot contain question answers")
		}
		return nil
	}
	if len(a.Answers) != len(p.Questions) {
		return fmt.Errorf("answer every question")
	}
	for _, q := range p.Questions {
		answers := a.Answers[q.ID]
		if len(answers) == 0 || len(answers) > 30 || (!q.MultiSelect && len(answers) != 1) {
			return fmt.Errorf("invalid question selection")
		}
		for _, answer := range answers {
			if strings.TrimSpace(answer) == "" || len(answer) > 16384 || strings.ContainsRune(answer, '\x00') {
				return fmt.Errorf("question answer is empty or too long")
			}
		}
	}
	return nil
}

// SuspendedInteraction 是原生工具收尾后持久化的待答请求；调用标识只用于关联，不写回模型历史。
type SuspendedInteraction struct {
	CallID string `json:"call_id"`
	InteractionPrompt
}

func (p SuspendedInteraction) Validate() error {
	if p.CallID == "" || len(p.CallID) > 256 || strings.ContainsRune(p.CallID, '\x00') {
		return fmt.Errorf("invalid native interaction call id")
	}
	if p.Kind != "plan" && p.Kind != "question" {
		return fmt.Errorf("unsupported suspended interaction kind")
	}
	if p.Kind == "plan" && strings.TrimSpace(p.PlanMarkdown) == "" {
		return fmt.Errorf("plan Markdown is required")
	}
	return p.InteractionPrompt.Validate()
}

func (p InteractionPrompt) ValidateContinuation(a InteractionAnswer) error {
	if len(a.Feedback) > 32768 || strings.ContainsRune(a.Feedback, '\x00') {
		return fmt.Errorf("invalid interaction feedback")
	}
	if a.Decision == "cancelled" {
		if len(a.Answers) != 0 || a.Feedback != "" {
			return fmt.Errorf("cancelled interaction cannot contain answers")
		}
		return nil
	}
	if p.Kind == "plan" {
		if a.Decision != "approved" && a.Decision != "keep_planning" {
			return fmt.Errorf("choose approved or keep_planning")
		}
		if len(a.Answers) != 0 {
			return fmt.Errorf("plan cannot contain question answers")
		}
		return nil
	}
	if a.Feedback != "" || a.Decision != "allow" {
		return fmt.Errorf("answer the question or cancel the suspended task")
	}
	return p.ValidateAnswer(a)
}
