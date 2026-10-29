package openviking

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

type PlatformEvent struct {
	ID          string          `json:"id"`
	WorkspaceID string          `json:"workspace_id"`
	AgentID     string          `json:"agent_id"`
	ChatID      string          `json:"chat_session_id"`
	IssueID     string          `json:"issue_id"`
	User        string          `json:"target_user"`
	Kind        string          `json:"kind"`
	Payload     json.RawMessage `json:"payload"`
}

func ConversationSession(agentID, chatID, issueID, taskID string) string {
	key := taskID
	if chatID != "" {
		key = "chat-" + chatID
	} else if issueID != "" {
		key = "issue-" + issueID
	}
	return "multica-" + SHA([]byte(agentID + "\x00" + key))[:40]
}

func PersistPlatformEvent(root string, event PlatformEvent) (*Journal, error) {
	marker := "platform-" + SHA([]byte(event.ID)) + ".json"
	raw, err := os.ReadFile(filepath.Join(root, marker))
	if err == nil {
		var saved struct {
			Dir string `json:"dir"`
		}
		if err = json.Unmarshal(raw, &saved); err != nil {
			return nil, err
		}
		metaRaw, err := os.ReadFile(filepath.Join(root, saved.Dir, "metadata.json"))
		if err != nil {
			return nil, err
		}
		var meta Metadata
		if err = json.Unmarshal(metaRaw, &meta); err != nil {
			return nil, err
		}
		return &Journal{Dir: filepath.Join(root, saved.Dir), Meta: meta}, nil
	}
	if !os.IsNotExist(err) {
		return nil, err
	}
	provider := "multica-" + event.Kind
	if event.AgentID == "" {
		provider = "multica-resource"
	}
	meta := Metadata{AgentID: event.AgentID, User: event.User, Session: ConversationSession(event.AgentID, event.ChatID, event.IssueID, event.ID), Provider: provider, TaskID: "platform-" + event.ID, WorkspaceID: event.WorkspaceID, ChatID: event.ChatID, IssueID: event.IssueID, PlatformEventID: event.ID, SourcePayload: event.Payload}
	j, err := Begin(root, meta)
	if err != nil {
		return nil, err
	}
	if err = writeJSONFile(root, marker, map[string]string{"dir": filepath.Base(j.Dir)}); err != nil {
		j.file.Close()
		return nil, err
	}
	if err = j.Finish(map[string]string{"platform_event": event.ID}); err != nil {
		return nil, err
	}
	return j, nil
}

func (j *Journal) PlatformAttachments() error {
	if j.Meta.PlatformEventID == "" {
		return nil
	}
	var event struct {
		Row struct {
			ID          string `json:"id"`
			Filename    string `json:"filename"`
			ContentType string `json:"content_type"`
		} `json:"row"`
	}
	if err := json.Unmarshal(j.Meta.SourcePayload, &event); err != nil {
		return err
	}
	if j.Meta.Provider == "multica-attachment" || j.Meta.Provider == "multica-resource" {
		if event.Row.Filename != "" {
			return j.WantAttachment(event.Row.ID, event.Row.Filename, event.Row.ContentType)
		}
	}
	return nil
}

func PlatformComplete(j *Journal) (bool, error) {
	raw, err := os.ReadFile(filepath.Join(j.Dir, "sync.json"))
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var state SyncState
	if err = json.Unmarshal(raw, &state); err != nil {
		return false, err
	}
	if state.Error != "" {
		return false, fmt.Errorf("%s", state.Error)
	}
	return state.Done, nil
}
