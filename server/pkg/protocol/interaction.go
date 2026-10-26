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
	Question    string              `json:"question"`
	Options     []InteractionOption `json:"options,omitempty"`
	MultiSelect bool                `json:"multi_select,omitempty"`
}

type InteractionPrompt struct {
	Kind      string                `json:"kind"`
	Title     string                `json:"title"`
	Questions []InteractionQuestion `json:"questions,omitempty"`
}

type InteractionAnswer struct {
	Decision string              `json:"decision"`
	Answers  map[string][]string `json:"answers,omitempty"`
}

type TaskInteraction struct {
	ID        string             `json:"id"`
	TaskID    string             `json:"task_id"`
	Provider  string             `json:"provider"`
	Prompt    InteractionPrompt  `json:"prompt"`
	Status    string             `json:"status"`
	Response  *InteractionAnswer `json:"response,omitempty"`
	CreatedAt string             `json:"created_at"`
	ExpiresAt string             `json:"expires_at"`
}

func (p InteractionPrompt) Validate() error {
	if p.Kind != "question" && p.Kind != "approval" && p.Kind != "plan" {
		return fmt.Errorf("unsupported interaction kind")
	}
	if strings.TrimSpace(p.Title) == "" || len(p.Title) > 32768 {
		return fmt.Errorf("interaction title is required and must fit 32768 bytes")
	}
	if len(p.Questions) > 10 || (p.Kind == "question" && len(p.Questions) == 0) {
		return fmt.Errorf("invalid interaction question count")
	}
	seen := map[string]bool{}
	for _, q := range p.Questions {
		if q.ID == "" || len(q.ID) > 128 || strings.ContainsRune(q.ID, '\x00') || seen[q.ID] || strings.TrimSpace(q.Question) == "" || len(q.Question) > 16384 || len(q.Options) > 30 {
			return fmt.Errorf("invalid interaction question")
		}
		seen[q.ID] = true
		for _, option := range q.Options {
			if strings.TrimSpace(option.Label) == "" || len(option.Label) > 1024 || len(option.Description) > 4096 {
				return fmt.Errorf("invalid interaction option")
			}
		}
	}
	return nil
}

func (p InteractionPrompt) ValidateAnswer(a InteractionAnswer) error {
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
