package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"

	"github.com/multica-ai/multica/server/internal/skill"
)

var errSkillAssetVersionChanged = errors.New("skill attachment version changed")

func (c *Client) downloadSkillAssets(ctx context.Context, runtimeID, taskID string, bundle *SkillData) error {
	for i := range bundle.Files {
		f := &bundle.Files[i]
		if f.Kind != "binary" {
			continue
		}
		if f.SizeBytes < 0 || f.SizeBytes > skill.MaxFileBytes || f.FileID == "" {
			return fmt.Errorf("invalid skill attachment metadata: %s", f.Path)
		}
		endpoint := fmt.Sprintf("/api/daemon/runtimes/%s/tasks/%s/skill-files/%s/content?sha256=%s", url.PathEscape(runtimeID), url.PathEscape(taskID), url.PathEscape(f.FileID), url.QueryEscape(f.SHA256))
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+endpoint, nil)
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+c.token)
		c.setIdentityHeaders(req)
		resp, err := c.bundleClient.Do(req)
		if err != nil {
			return err
		}
		data, readErr := io.ReadAll(io.LimitReader(resp.Body, skill.MaxFileBytes+1))
		resp.Body.Close()
		if resp.StatusCode == http.StatusConflict {
			return errSkillAssetVersionChanged
		}
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("skill attachment %s: HTTP %d", f.Path, resp.StatusCode)
		}
		if readErr != nil {
			return readErr
		}
		sum := sha256.Sum256(data)
		if int64(len(data)) != f.SizeBytes || "sha256:"+hex.EncodeToString(sum[:]) != f.SHA256 {
			return fmt.Errorf("skill attachment checksum mismatch: %s", f.Path)
		}
		f.Data = data
	}
	return nil
}
