package daemon

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

func (c *Client) taskInteractionRequester(taskID, provider string) func(context.Context, protocol.InteractionPrompt) (protocol.InteractionAnswer, error) {
	return func(ctx context.Context, prompt protocol.InteractionPrompt) (protocol.InteractionAnswer, error) {
		id, err := uuid.NewV7()
		if err != nil {
			return protocol.InteractionAnswer{}, err
		}
		base := fmt.Sprintf("/api/daemon/tasks/%s/interactions", taskID)
		answered := false
		defer func() {
			if answered {
				return
			}
			cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
			defer cancel()
			_ = c.postJSON(cleanup, base+"/"+id.String()+"/expire", map[string]any{}, nil)
		}()
		var interaction protocol.TaskInteraction
		if err = c.postJSONWithRetry(ctx, base, map[string]any{"id": id.String(), "provider": provider, "prompt": prompt}, &interaction, []time.Duration{0, 100 * time.Millisecond, 300 * time.Millisecond}); err != nil {
			return protocol.InteractionAnswer{}, err
		}
		expires, err := time.Parse(time.RFC3339Nano, interaction.ExpiresAt)
		if err != nil || !expires.After(time.Now()) || interaction.ID != id.String() || interaction.TaskID != taskID {
			return protocol.InteractionAnswer{}, errors.New("invalid runtime interaction identity")
		}
		waitCtx, cancel := context.WithTimeout(ctx, 24*time.Hour)
		defer cancel()
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			switch interaction.Status {
			case "responded":
				if interaction.Response == nil {
					return protocol.InteractionAnswer{}, errors.New("interaction answer is missing")
				}
				if err := prompt.ValidateAnswer(*interaction.Response); err != nil {
					return protocol.InteractionAnswer{}, err
				}
				answered = true
				return *interaction.Response, nil
			case "pending":
			default:
				return protocol.InteractionAnswer{}, errors.New("runtime interaction expired")
			}
			select {
			case <-waitCtx.Done():
				return protocol.InteractionAnswer{}, waitCtx.Err()
			case <-ticker.C:
			}
			poll, pollCancel := context.WithTimeout(waitCtx, 5*time.Second)
			var next protocol.TaskInteraction
			err = c.postJSON(poll, base+"/"+id.String()+"/lease", map[string]any{}, &next)
			pollCancel()
			if err != nil {
				var requestErr *requestError
				if errors.As(err, &requestErr) && (requestErr.StatusCode == http.StatusUnauthorized || requestErr.StatusCode == http.StatusForbidden || requestErr.StatusCode == http.StatusNotFound) {
					return protocol.InteractionAnswer{}, err
				}
				continue
			}
			if next.ID != id.String() || next.TaskID != taskID {
				return protocol.InteractionAnswer{}, errors.New("runtime interaction changed identity")
			}
			interaction = next
		}
	}
}
