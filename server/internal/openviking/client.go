// Package openviking 提供平台归档、召回和原生文件传输共用的客户端。
package openviking

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

type Client struct {
	CanCommit func(context.Context, Metadata) (bool, error)
	URL       string
	Key       string
	HTTP      *http.Client
}

func Enabled() bool { return os.Getenv("MULTICA_OPENVIKING_ENABLED") == "true" }

func FromEnv() *Client {
	return &Client{URL: strings.TrimRight(os.Getenv("MULTICA_OPENVIKING_URL"), "/"), Key: os.Getenv("MULTICA_OPENVIKING_API_KEY"), HTTP: &http.Client{Timeout: 45 * time.Second}}
}

var userPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,127}$`)

func User(agentID, configured string) (string, error) {
	user := configured
	if user == "" {
		user = "agent-" + agentID
	}
	if !userPattern.MatchString(user) || user == "default" {
		return "", fmt.Errorf("invalid OpenViking Agent user %q", user)
	}
	return user, nil
}

type HTTPError struct {
	Code int
	Body string
}

func (e *HTTPError) Error() string { return fmt.Sprintf("OpenViking HTTP %d: %s", e.Code, e.Body) }

func (c *Client) Request(ctx context.Context, user, method, path, contentType string, body io.Reader) ([]byte, error) {
	if c.URL == "" {
		return nil, fmt.Errorf("MULTICA_OPENVIKING_URL is required")
	}
	req, err := http.NewRequestWithContext(ctx, method, c.URL+path, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-OpenViking-Account", "default")
	req.Header.Set("X-OpenViking-User", user)
	if c.Key != "" {
		req.Header.Set("Authorization", "Bearer "+c.Key)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, &HTTPError{resp.StatusCode, string(data)}
	}
	return io.ReadAll(resp.Body)
}

func (c *Client) JSON(ctx context.Context, user, method, path string, body any, result any) error {
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(raw)
	}
	raw, err := c.Request(ctx, user, method, path, "application/json", reader)
	if err != nil {
		return err
	}
	var envelope struct {
		Status string          `json:"status"`
		Result json.RawMessage `json:"result"`
		Error  json.RawMessage `json:"error"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return err
	}
	if envelope.Status != "ok" {
		return fmt.Errorf("OpenViking response: %s", raw)
	}
	if result == nil {
		return nil
	}
	return json.Unmarshal(envelope.Result, result)
}

func (c *Client) Health(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	raw, err := c.Request(ctx, "default", "GET", "/health", "", nil)
	if err != nil {
		return err
	}
	var health struct {
		Healthy bool `json:"healthy"`
	}
	if err := json.Unmarshal(raw, &health); err != nil {
		return err
	}
	if !health.Healthy {
		return fmt.Errorf("OpenViking is unhealthy")
	}
	_, err = c.Request(ctx, "multica", "GET", "/api/v1/sessions", "", nil)
	return err
}

func (c *Client) Download(ctx context.Context, user, uri string) ([]byte, error) {
	return c.Request(ctx, user, "GET", "/api/v1/content/download?uri="+url.QueryEscape(uri), "", nil)
}

func (c *Client) Upload(ctx context.Context, user, name string, pack io.Reader) (string, error) {
	// 流式上传避免附件包在内存中再复制一份。
	reader, writer := io.Pipe()
	form := multipart.NewWriter(writer)
	done := make(chan error, 1)
	go func() {
		part, err := form.CreateFormFile("file", name)
		if err == nil {
			_, err = io.Copy(part, pack)
		}
		if err == nil {
			err = form.Close()
		}
		_ = writer.CloseWithError(err)
		done <- err
	}()
	raw, err := c.Request(ctx, user, "POST", "/api/v1/resources/temp_upload", form.FormDataContentType(), reader)
	_ = reader.Close()
	writeErr := <-done
	if err != nil {
		return "", err
	}
	if writeErr != nil {
		return "", writeErr
	}
	var upload struct {
		Result struct {
			ID string `json:"temp_file_id"`
		} `json:"result"`
	}
	if err := json.Unmarshal(raw, &upload); err != nil {
		return "", err
	}
	if upload.Result.ID == "" {
		return "", fmt.Errorf("OpenViking upload did not return temp_file_id")
	}
	return upload.Result.ID, nil
}

type Entry struct {
	URI   string `json:"uri"`
	Name  string `json:"name"`
	IsDir bool   `json:"isDir"`
}

func (c *Client) List(ctx context.Context, user, uri string) ([]Entry, error) {
	entries := []Entry{}
	for offset := 0; ; {
		raw, err := c.Request(ctx, user, "GET", fmt.Sprintf("/api/v1/fs/ls?output=original&show_all_hidden=true&limit=500&offset=%d&uri=%s", offset, url.QueryEscape(uri)), "", nil)
		if e, ok := err.(*HTTPError); ok && e.Code == 404 {
			return entries, nil
		}
		if err != nil {
			return nil, err
		}
		var page struct {
			Result  []Entry `json:"result"`
			HasMore bool    `json:"has_more"`
		}
		if err := json.Unmarshal(raw, &page); err != nil {
			return nil, err
		}
		entries = append(entries, page.Result...)
		if !page.HasMore {
			return entries, nil
		}
		if len(page.Result) == 0 {
			return nil, fmt.Errorf("OpenViking returned an empty nonterminal listing")
		}
		offset += len(page.Result)
	}
}

func (c *Client) Recall(ctx context.Context, user, query string) (string, error) {
	var out strings.Builder
	// dev 模式的身份头不会限制 ROOT；检索范围必须显式指定。
	for _, uri := range []string{"viking://resources", "viking://user/" + user + "/memories"} {
		var result json.RawMessage
		err := c.JSON(ctx, user, "POST", "/api/v1/search/find", map[string]any{"query": query, "target_uri": uri, "limit": 8, "read_content": true, "filter": map[string]any{"op": "must_not", "field": "uri", "conds": []string{"viking://resources/migration"}, "para": "-d=-1"}}, &result)
		if e, ok := err.(*HTTPError); ok && e.Code == 404 {
			continue
		}
		if err != nil {
			return "", err
		}
		if out.Len() != 0 {
			out.WriteByte('\n')
		}
		out.Write(result)
	}
	text := out.String()
	// 仅限制传给模型的召回上下文；原始归档没有此裁剪。
	if len(text) > 24000 {
		text = text[:24000]
		for !utf8.ValidString(text) {
			text = text[:len(text)-1]
		}
	}
	return text, nil
}
