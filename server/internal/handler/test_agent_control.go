package handler

import (
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/multica-ai/multica/server/internal/auth"
	"github.com/multica-ai/multica/server/internal/testagents"
)

// 测试控制入口只属于安装记录指定的生产工作区；浏览器不能提供连接地址或凭据。
func (h *Handler) testAgentOperator(w http.ResponseWriter, r *http.Request) (string, bool) {
	ws, ok := h.deliveryOperator(w, r)
	if !ok {
		return "", false
	}
	if auth.EnvironmentID() != "prod" || ws != os.Getenv("MULTICA_TEST_AGENT_CONTROL_WORKSPACE_ID") {
		writeError(w, 403, "当前工作区未启用测试环境管理")
		return "", false
	}
	return ws, true
}

type testAgentPolicy = testagents.Policy

func testAgentEnvironment(name string) bool { return testagents.Environment(name) }

func (h *Handler) TestAgentControl(w http.ResponseWriter, r *http.Request) {
	ws, ok := h.testAgentOperator(w, r)
	if !ok {
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	if r.Method == "GET" {
		var rows json.RawMessage
		err := h.DB.QueryRow(r.Context(), `SELECT COALESCE(jsonb_agg(to_jsonb(t) ORDER BY name),'[]') FROM test_agent_control t WHERE workspace_id=$1`, ws).Scan(&rows)
		if err != nil {
			writeError(w, 500, "读取测试环境失败")
			return
		}
		writeJSON(w, 200, rows)
		return
	}
	var body struct {
		Action   string          `json:"action"`
		Name     string          `json:"name"`
		Revision int64           `json:"revision"`
		Policy   testAgentPolicy `json:"policy"`
		AgentID  string          `json:"agent_id"`
		Observed json.RawMessage `json:"observed"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 262144))
	dec.DisallowUnknownFields()
	if dec.Decode(&body) != nil {
		writeError(w, 400, "测试环境配置无效")
		return
	}
	if body.Name != "default" && !testAgentEnvironment(body.Name) {
		writeError(w, 400, "未知测试环境")
		return
	}
	if body.Action == "report" {
		if body.Name == "default" || !json.Valid(body.Observed) {
			writeError(w, 400, "环境报告无效")
			return
		}
		// 仅回报观测数据，不覆盖用户在观测期间提交的新版本配置。
		_, err := h.DB.Exec(r.Context(), `INSERT INTO test_agent_control(workspace_id,name,observed,observed_at,desired) VALUES($1,$2,$3,now(),COALESCE((SELECT desired FROM test_agent_control WHERE workspace_id=$1 AND name='default'),'{"policy":{"provider":"codebuddy","model":"deepseek-v4.1-flash","thinking_level":"","service_tier":""},"overrides":{}}'::jsonb)) ON CONFLICT(workspace_id,name) DO UPDATE SET observed=EXCLUDED.observed,observed_at=now()`, ws, body.Name, body.Observed)
		if err != nil {
			writeError(w, 500, "保存测试环境报告失败")
			return
		}
		writeJSON(w, 200, map[string]bool{"ok": true})
		return
	}
	if body.Action != "configure" || !body.Policy.Valid() {
		writeError(w, 400, "仅支持 Flash 或 Codex gpt-6.1-sol / low / 正常速度")
		return
	}
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, 500, "保存配置失败")
		return
	}
	defer tx.Rollback(r.Context())
	var revision int64
	var desired struct {
		Policy    testAgentPolicy            `json:"policy"`
		Overrides map[string]testAgentPolicy `json:"overrides"`
	}
	var observed struct {
		Agents []struct {
			ID string `json:"id"`
		} `json:"agents"`
	}
	// default 控制所有已登记环境及后续新建 Agent；并发编辑通过版本号显式拒绝。
	if body.Name == "default" {
		_, err = tx.Exec(r.Context(), `INSERT INTO test_agent_control(workspace_id,name) VALUES($1,'default') ON CONFLICT DO NOTHING`, ws)
	}
	var raw, obs []byte
	if err == nil {
		err = tx.QueryRow(r.Context(), `SELECT revision,desired,observed FROM test_agent_control WHERE workspace_id=$1 AND name=$2 FOR UPDATE`, ws, body.Name).Scan(&revision, &raw, &obs)
	}
	if err != nil {
		writeError(w, 404, "环境尚未登记")
		return
	}
	if revision != body.Revision {
		writeError(w, 409, "配置已被更新，请刷新后重试")
		return
	}
	if json.Unmarshal(raw, &desired) != nil || json.Unmarshal(obs, &observed) != nil {
		writeError(w, 500, "配置读取失败")
		return
	}
	if body.AgentID != "" {
		found := false
		for _, a := range observed.Agents {
			if a.ID == body.AgentID {
				found = true
			}
		}
		if !found {
			writeError(w, 400, "Agent 不属于当前环境")
			return
		}
		if desired.Overrides == nil {
			desired.Overrides = map[string]testAgentPolicy{}
		}
		desired.Overrides[body.AgentID] = body.Policy
	} else {
		desired.Policy = body.Policy
		desired.Overrides = map[string]testAgentPolicy{}
	}
	raw, _ = json.Marshal(desired)
	_, err = tx.Exec(r.Context(), `UPDATE test_agent_control SET desired=$3,revision=revision+1,updated_by=$4,updated_at=$5 WHERE workspace_id=$1 AND name=$2`, ws, body.Name, raw, requestUserID(r), time.Now())
	if err == nil && body.Name == "default" {
		_, err = tx.Exec(r.Context(), `UPDATE test_agent_control SET desired=$2,revision=revision+1,updated_by=$3,updated_at=now() WHERE workspace_id=$1 AND name<>'default'`, ws, raw, requestUserID(r))
	}
	if err != nil || tx.Commit(r.Context()) != nil {
		writeError(w, 500, "保存配置失败")
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// 新建测试 Agent 使用本环境已核对的默认值；生产创建链路不读取测试设置。
func (h *Handler) applyTestAgentDefault(w http.ResponseWriter, r *http.Request, ws string, req *CreateAgentRequest) bool {
	selection, enabled, err := testagents.Resolve(r.Context(), h.DB, ws, "")
	if err != nil {
		writeError(w, 503, err.Error())
		return false
	}
	if !enabled {
		return true
	}
	for _, arg := range req.CustomArgs {
		if strings.HasPrefix(arg, "-m") || strings.HasPrefix(arg, "-c") || strings.HasPrefix(arg, "--model") || strings.HasPrefix(arg, "--config") || strings.Contains(arg, "fast_mode") {
			writeError(w, 400, "测试 Agent 的模型启动参数由生产测试设置管理")
			return false
		}
	}
	req.RuntimeID = selection.RuntimeID
	req.Model = selection.Model
	req.ThinkingLevel = selection.Thinking
	req.ServiceTier = selection.Tier
	return true
}
