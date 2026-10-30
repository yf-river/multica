package handler

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"

	"github.com/multica-ai/multica/server/internal/openviking"
)

// OpenVikingStudio 使用现有 Multica 会话，直接代理原生 Studio 和 API。
// 这是平台管理入口；Agent 的任务 token 不允许访问此入口。
func (h *Handler) OpenVikingStudio(w http.ResponseWriter, r *http.Request) {
	if !openviking.Enabled() {
		writeError(w, 503, "OpenViking is disabled in this environment")
		return
	}
	c := openviking.FromEnv()
	target, err := url.Parse(c.URL)
	if err != nil || target.Host == "" {
		writeError(w, 503, "OpenViking is not configured")
		return
	}
	nonceBytes := make([]byte, 16)
	if _, err = rand.Read(nonceBytes); err != nil {
		writeError(w, 500, "could not initialize Studio")
		return
	}
	nonce := hex.EncodeToString(nonceBytes)
	policy := "default-src 'self'; script-src 'self' 'nonce-" + nonce + "'; style-src 'self' 'unsafe-inline'; img-src 'self' https: data: blob:; font-src 'self' data:; connect-src 'self' ws: wss:; worker-src 'self' blob:; frame-ancestors 'self'; object-src 'none'; base-uri 'self'; form-action 'self'"
	w.Header().Set("Content-Security-Policy", policy)
	w.Header().Set("X-Frame-Options", "SAMEORIGIN")
	p := httputil.NewSingleHostReverseProxy(target)
	director := p.Director
	p.Director = func(req *http.Request) {
		req.URL.Path = strings.TrimPrefix(req.URL.Path, "/api/openviking")
		director(req)
		req.Host = target.Host
		req.Header.Del("Cookie")
		req.Header.Del("Authorization")
		req.Header.Del("X-API-Key")
		req.Header.Del("X-OpenViking-Actor-Peer")
		req.Header.Set("X-OpenViking-Account", "default")
		req.Header.Del("X-OpenViking-Role")
		if req.Header.Get("X-OpenViking-User") == "" {
			req.Header.Set("X-OpenViking-User", "default")
		}
		if c.Key != "" {
			req.Header.Set("Authorization", "Bearer "+c.Key)
		}
		req.Header.Set("Accept-Encoding", "identity")
	}
	p.ModifyResponse = func(resp *http.Response) error {
		resp.Header.Del("Set-Cookie")
		resp.Header.Del("Content-Security-Policy")
		resp.Header.Del("X-Frame-Options")
		if !strings.Contains(resp.Header.Get("Content-Type"), "text/html") || !(resp.Request.URL.Path == "/studio" || strings.HasPrefix(resp.Request.URL.Path, "/studio/")) {
			return nil
		}
		data, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			return err
		}
		// 只初始化本站的原生连接配置；不向浏览器暴露 OpenViking 凭据。
		profiles, err := h.openVikingStudioProfiles(r)
		if err != nil {
			return err
		}
		encoded, err := json.Marshal(profiles)
		if err != nil {
			return err
		}
		bootstrap := `<script nonce="` + nonce + `">(function(){const managed=` + string(encoded) + `;managed.forEach(p=>p.connection.baseUrl=location.origin+'/api/openviking');const key='ov_console_connection_profiles';let previous;try{previous=JSON.parse(localStorage.getItem(key)||'null')}catch{};const extra=(Array.isArray(previous?.profiles)?previous.profiles:[]).filter(p=>p.id!=='multica'&&!p.id?.startsWith('multica-agent-')&&p.id!=='multica-history-default');const profiles=[...managed,...extra];const active=managed.some(p=>p.id===previous?.activeId)?previous.activeId:'multica';localStorage.setItem(key,JSON.stringify({activeId:active,profiles}));localStorage.setItem('ov_console_connection',JSON.stringify(managed.find(p=>p.id===active).connection));localStorage.setItem('ov_studio_setup','complete');sessionStorage.setItem('ov_studio_workspace_selected','selected');})();</script>`

		data = bytes.Replace(data, []byte("<head>"), []byte("<head>"+bootstrap), 1)
		resp.Body = io.NopCloser(bytes.NewReader(data))
		resp.ContentLength = int64(len(data))
		resp.Header.Set("Content-Length", strconv.Itoa(len(data)))
		resp.Header.Set("Cache-Control", "no-store")
		resp.Header.Del("ETag")
		return nil
	}
	p.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		writeError(w, 502, fmt.Sprintf("OpenViking is unavailable: %v", err))
	}
	p.ServeHTTP(w, r)
}

// 原生会话与经验页按当前 user 展示；连接列表直接复用 Agent 的运行配置。
func (h *Handler) openVikingStudioProfiles(r *http.Request) ([]map[string]any, error) {
	profiles := []map[string]any{}
	seen := map[string]bool{}
	add := func(id, name, user string) {
		if seen[user] {
			return
		}
		seen[user] = true
		profiles = append(profiles, map[string]any{"id": id, "name": name, "connection": map[string]string{"baseUrl": "/api/openviking", "accountId": "default", "userId": user, "apiKey": "", "adminApiKey": "", "serviceSelection": "opensource"}})
	}
	add("multica", "Multica", "multica")
	rows, err := h.DB.Query(r.Context(), `SELECT id::text,name,COALESCE(custom_env->>'MULTICA_OPENVIKING_USER','') FROM agent WHERE archived_at IS NULL ORDER BY name,id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id, name, configured string
		if err = rows.Scan(&id, &name, &configured); err != nil {
			return nil, err
		}
		user, err := openviking.User(id, configured)
		if err != nil {
			continue
		}
		add("multica-agent-"+id, name, user)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	add("multica-history-default", "历史 default", "default")
	return profiles, nil
}
