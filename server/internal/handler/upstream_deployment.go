package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"time"

	"github.com/multica-ai/multica/server/internal/util"
	"github.com/multica-ai/multica/server/pkg/dbid"
)

func deploymentConfiguration() string { return os.Getenv("MULTICA_UPSTREAM_DEPLOYMENT_CONFIG") }

func (h *Handler) GetUpstreamDeployments(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := h.upstreamScope(w, r, false); !ok {
		return
	}
	if deploymentConfiguration() != "" {
		h.reconcileUpstreamDeployments(r.Context())
	}
	rows, err := h.DB.Query(r.Context(), `SELECT id::text,action,version,status,result,error,created_at,updated_at FROM upstream_deployment WHERE project_id=$1 ORDER BY created_at DESC LIMIT 30`, h.upstreamService(r).Project())
	if err != nil {
		writeError(w, 500, "读取部署记录失败")
		return
	}
	defer rows.Close()
	jobs := []map[string]any{}
	for rows.Next() {
		var id, action, version, status, failure string
		var result []byte
		var created, updated time.Time
		if err = rows.Scan(&id, &action, &version, &status, &result, &failure, &created, &updated); err != nil {
			writeError(w, 500, "读取部署记录失败")
			return
		}
		jobs = append(jobs, map[string]any{"id": id, "action": action, "version": version, "status": status, "result": deploymentResult(action, result), "error": failure, "created_at": created, "updated_at": updated})
	}
	writeJSON(w, 200, map[string]any{"available": deploymentConfiguration() != "" && h.upstreamService(r).Project() == "openviking", "jobs": jobs})
}

var deploymentVersion = regexp.MustCompile(`^[0-9]+(?:\.[0-9]+){1,3}$`)

func (h *Handler) PrepareUpstreamDeployment(w http.ResponseWriter, r *http.Request) {
	user, _, ok := h.upstreamScope(w, r, true)
	if !ok {
		return
	}
	if h.upstreamService(r).Project() != "openviking" || deploymentConfiguration() == "" {
		writeError(w, 409, "该项目尚未配置本机部署目标")
		return
	}
	var body struct {
		Action  string `json:"action"`
		Version string `json:"version"`
	}
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body) != nil || (body.Action != "prepare" && body.Action != "deploy" && body.Action != "recover") || (body.Version != "" && !deploymentVersion.MatchString(body.Version)) {
		writeError(w, 400, "部署动作或版本无效")
		return
	}
	if body.Action == "deploy" && body.Version == "" {
		writeError(w, 400, "部署必须指定已验证版本")
		return
	}
	id, err := h.startUpstreamDeployment(r.Context(), user, body.Action, body.Version)
	if err != nil {
		writeError(w, 409, "已有部署操作正在执行，或操作无法保存")
		return
	}
	writeJSON(w, 202, map[string]string{"id": id, "status": "pending"})
}

func (h *Handler) startUpstreamDeployment(ctx context.Context, user, action, version string) (string, error) {
	id := util.UUIDToString(dbid.NewV7())
	_, err := h.DB.Exec(ctx, `INSERT INTO upstream_deployment(id,project_id,action,version,created_by) VALUES($1,'openviking',$2,$3,$4)`, id, action, version, user)
	if err != nil {
		return "", err
	}
	go h.executeUpstreamDeployment(id, action, version)
	return id, nil
}

func (h *Handler) executeUpstreamDeployment(id, action, version string) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()
	if _, err := h.DB.Exec(ctx, `UPDATE upstream_deployment SET status='running',updated_at=now() WHERE id=$1 AND status='pending'`, id); err != nil {
		return
	}
	command, err := deploymentCommand(ctx, action, version, id)
	if err != nil {
		_, _ = h.DB.Exec(ctx, `UPDATE upstream_deployment SET status='failed',error='部署程序未安装',updated_at=now() WHERE id=$1`, id)
		return
	}
	output, runErr := command.Output()
	status := "succeeded"
	failure := ""
	result := json.RawMessage(`{}`)
	if runErr != nil {
		status = "failed"
		failure = "部署执行失败，请检查本机候选验证记录"
	} else if json.Valid(output) {
		result = output
	} else {
		status = "failed"
		failure = "部署返回了无效结果"
	}
	if action == "deploy" && status == "succeeded" {
		var value struct {
			Status string `json:"status"`
		}
		if json.Unmarshal(result, &value) == nil && value.Status == "deployed_pending_acceptance" {
			status = "pending_acceptance"
		}
	}
	finalCtx, done := context.WithTimeout(context.Background(), 10*time.Second)
	defer done()
	_, _ = h.DB.Exec(finalCtx, `UPDATE upstream_deployment SET status=$2,result=$3,error=$4,updated_at=now() WHERE id=$1`, id, status, result, failure)
}

func deploymentCommand(ctx context.Context, action, version, id string) (*exec.Cmd, error) {
	executable, err := os.Executable()
	if err != nil {
		return nil, err
	}
	candidates := []string{filepath.Join(filepath.Dir(executable), "../../operations/openviking-deployment.py"), filepath.Join("scripts", "openviking-deployment.py")}
	for _, script := range candidates {
		if info, e := os.Stat(script); e == nil && !info.IsDir() {
			args := []string{script, action, "--config", deploymentConfiguration()}
			if version != "" {
				args = append(args, "--version", version)
			}
			if id != "" {
				args = append(args, "--operation-id", id)
			}
			return exec.CommandContext(ctx, "python3", args...), nil
		}
	}
	return nil, fmt.Errorf("deployment program unavailable")
}

// 本机操作文件是进程重启后的执行事实；只恢复记录，不重复切换服务。
func (h *Handler) reconcileUpstreamDeployments(ctx context.Context) {
	rows, err := h.DB.Query(ctx, `SELECT id::text,action,created_at FROM upstream_deployment WHERE project_id='openviking' AND status IN ('pending','running')`)
	if err != nil {
		return
	}
	type pending struct {
		id, action string
		created    time.Time
	}
	jobs := []pending{}
	for rows.Next() {
		var job pending
		if rows.Scan(&job.id, &job.action, &job.created) == nil {
			jobs = append(jobs, job)
		}
	}
	rows.Close()
	for _, job := range jobs {
		readCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		command, err := deploymentCommand(readCtx, "status", "", job.id)
		if err != nil {
			cancel()
			continue
		}
		output, err := command.Output()
		cancel()
		if err != nil {
			continue
		}
		var value struct {
			State  string          `json:"state"`
			Result json.RawMessage `json:"result"`
			Error  string          `json:"error"`
		}
		if json.Unmarshal(output, &value) != nil {
			continue
		}
		status, failure := "", ""
		result := json.RawMessage(`{}`)
		switch value.State {
		case "completed":
			status = "succeeded"
			result = value.Result
			var deployed struct {
				Status string `json:"status"`
			}
			if job.action == "deploy" && json.Unmarshal(result, &deployed) == nil && deployed.Status == "deployed_pending_acceptance" {
				status = "pending_acceptance"
			}
		case "failed":
			status = "failed"
			failure = value.Error
		case "interrupted":
			status = "interrupted"
			failure = "部署进程已中断，请检查服务状态并恢复或重试"
		case "missing":
			if time.Since(job.created) > time.Minute {
				status = "interrupted"
				failure = "操作尚未启动，请重试"
			}
		}
		if status != "" {
			_, _ = h.DB.Exec(ctx, `UPDATE upstream_deployment SET status=$2,result=$3,error=$4,updated_at=now() WHERE id=$1 AND status IN ('pending','running')`, job.id, status, result, failure)
		}
	}
}

// before 是恢复目标，after 是此次被替换的版本；历史记录也按此口径展示。
func deploymentResult(action string, raw []byte) json.RawMessage {
	if action == "recover" {
		var value map[string]json.RawMessage
		if json.Unmarshal(raw, &value) == nil && len(value["before"]) > 0 && string(value["before"]) != "null" {
			value["version"] = value["before"]
			if normalized, err := json.Marshal(value); err == nil {
				return normalized
			}
		}
	}
	return json.RawMessage(raw)
}
