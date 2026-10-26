package daemon

import (
	"encoding/json"
	"mime"
	"net/http"
	"path/filepath"

	"github.com/multica-ai/multica/server/pkg/dshprofile"
)

// 凭据或网络恢复后由本机操作者显式重试原候选；不更换制品、不改变发布授权。
func (d *Daemon) dshRetryHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		mediaType, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if r.Header.Get("Origin") != "" || mediaType != "application/json" {
			http.Error(w, "local JSON request required", http.StatusForbidden)
			return
		}
		root, _, enabled := dshSupplyConfig()
		if !enabled {
			http.Error(w, "managed DSH profile is disabled", http.StatusConflict)
			return
		}
		lease, err := dshprofile.Lock(filepath.Join(root, "preparation"), true)
		if err != nil {
			http.Error(w, "DSH profile preparation is busy", http.StatusConflict)
			return
		}
		defer lease.Close()
		status := readDshSupplyStatus(root)
		if status.Status != "degraded" || status.Identity == "" {
			http.Error(w, "DSH profile has no failed candidate", http.StatusConflict)
			return
		}
		status.Status = "pending"
		status.Error = ""
		if err = dshprofile.WriteJSON(root, "status.json", status); err != nil {
			http.Error(w, "cannot persist DSH retry", http.StatusInternalServerError)
			return
		}
		d.kickAgentDiscovery()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "pending"})
	}
}
