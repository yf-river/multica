package remotemcp

// 本文件职责：实现 MCP 的 OAuth 授权发现与换取流程——先按 RFC 9728 / RFC 8414
// （含 OIDC discovery 兜底）链式发现授权服务器，再用 RFC 7591 动态注册客户端，
// 最后走 PKCE authorization code 流程换 token。所有出站 URL 都套同一套公网
// HTTPS 校验，防 SSRF。

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// maxOAuthResponseBytes 限制 OAuth 各文档与 token 响应的大小。
const maxOAuthResponseBytes = 1 << 20

// resourceMetadataParameter 从 WWW-Authenticate 挑战头里抽出
// resource_metadata="..." 的值。正则大小写不敏感，允许参数在值前后带分隔符。
var resourceMetadataParameter = regexp.MustCompile(`(?i)(?:^|[, ]+)resource_metadata="([^"]+)"`)

// OAuthMetadata 是一次 MCP OAuth 发现结果的「安全且公开」的部分。
// token 与客户端密钥绝不进入这个值。
type OAuthMetadata struct {
	ResourceEndpoint      string
	AuthorizationEndpoint string
	TokenEndpoint         string
	RegistrationEndpoint  string
	Scopes                []string
	TokenAuthMethods      []string
}

// OAuthClientRegistration 是一次动态客户端注册（RFC 7591）的结果。
type OAuthClientRegistration struct {
	ClientID                string
	ClientSecret            string
	TokenEndpointAuthMethod string
}

// OAuthTokenResponse 是 token 端点返回的访问令牌（含可选的刷新令牌）。
type OAuthTokenResponse struct {
	AccessToken  string
	TokenType    string
	ExpiresIn    int64
	RefreshToken string
	Scope        string
}

// ValidateOAuthMetadata 把「SSRF 边界」同样施加到运维手工填写的进阶覆盖上，
// 与发现到的元数据一视同仁。
func ValidateOAuthMetadata(ctx context.Context, metadata OAuthMetadata) error {
	if metadata.AuthorizationEndpoint == "" || metadata.TokenEndpoint == "" {
		return errors.New("OAuth metadata is missing required endpoints")
	}
	for _, raw := range []string{metadata.AuthorizationEndpoint, metadata.TokenEndpoint, metadata.RegistrationEndpoint} {
		if raw == "" {
			continue
		}
		if _, err := validateOAuthURL(ctx, raw); err != nil {
			return err
		}
	}
	return nil
}

// protectedResourceMetadata 对应 RFC 9728 受保护资源元数据文档的线格式。
type protectedResourceMetadata struct {
	Resource             string   `json:"resource"`
	AuthorizationServers []string `json:"authorization_servers"`
	ScopesSupported      []string `json:"scopes_supported"`
}

// authorizationServerMetadata 对应 RFC 8414 授权服务器元数据文档的线格式。
type authorizationServerMetadata struct {
	AuthorizationEndpoint           string   `json:"authorization_endpoint"`
	TokenEndpoint                   string   `json:"token_endpoint"`
	RegistrationEndpoint            string   `json:"registration_endpoint"`
	CodeChallengeMethodsSupported   []string `json:"code_challenge_methods_supported"`
	TokenEndpointAuthMethodsSupport []string `json:"token_endpoint_auth_methods_supported"`
}

// DiscoverOAuth 走一遍 MCP 的授权发现链：先取受保护资源元数据（RFC 9728），
// 再取 OAuth 授权服务器元数据（RFC 8414，OIDC discovery 作为兼容兜底）。
// 每一个被拉取的 URL 都独立做「公网 HTTPS」校验，且所有 client 都禁用代理和
// 跨 host 重定向。
func DiscoverOAuth(ctx context.Context, rawEndpoint string, allowedHosts []string) (OAuthMetadata, error) {
	endpoint, err := ValidatePublicHTTPSEndpoint(ctx, rawEndpoint, allowedHosts, nil)
	if err != nil {
		return OAuthMetadata{}, err
	}
	metadataURL := probeResourceMetadataURL(ctx, endpoint)
	if metadataURL == "" {
		metadataURL = protectedResourceMetadataURL(endpoint)
	}
	resourceURL, err := validateOAuthURL(ctx, metadataURL)
	if err != nil {
		return OAuthMetadata{}, fmt.Errorf("protected resource metadata URL: %w", err)
	}
	var resource protectedResourceMetadata
	if err := getOAuthJSON(ctx, resourceURL, &resource); err != nil {
		return OAuthMetadata{}, fmt.Errorf("load protected resource metadata: %w", err)
	}
	if resource.Resource != "" && resource.Resource != endpoint.String() {
		return OAuthMetadata{}, errors.New("protected resource metadata does not match the MCP endpoint")
	}
	if len(resource.AuthorizationServers) == 0 {
		return OAuthMetadata{}, errors.New("protected resource metadata does not advertise an authorization server")
	}

	issuer, err := validateOAuthURL(ctx, resource.AuthorizationServers[0])
	if err != nil {
		return OAuthMetadata{}, fmt.Errorf("authorization server: %w", err)
	}
	var server authorizationServerMetadata
	var discoveryErr error
	for _, candidate := range authorizationMetadataURLs(issuer) {
		candidateURL, validateErr := validateOAuthURL(ctx, candidate)
		if validateErr != nil {
			discoveryErr = validateErr
			continue
		}
		if loadErr := getOAuthJSON(ctx, candidateURL, &server); loadErr == nil {
			discoveryErr = nil
			break
		} else {
			discoveryErr = loadErr
		}
	}
	if discoveryErr != nil {
		return OAuthMetadata{}, fmt.Errorf("load authorization server metadata: %w", discoveryErr)
	}
	if server.AuthorizationEndpoint == "" || server.TokenEndpoint == "" {
		return OAuthMetadata{}, errors.New("authorization server metadata is missing required endpoints")
	}
	if len(server.CodeChallengeMethodsSupported) > 0 && !containsString(server.CodeChallengeMethodsSupported, "S256") {
		return OAuthMetadata{}, errors.New("authorization server does not support PKCE S256")
	}
	metadata := OAuthMetadata{
		ResourceEndpoint:      endpoint.String(),
		AuthorizationEndpoint: server.AuthorizationEndpoint,
		TokenEndpoint:         server.TokenEndpoint,
		RegistrationEndpoint:  server.RegistrationEndpoint,
		Scopes:                append([]string(nil), resource.ScopesSupported...),
		TokenAuthMethods:      append([]string(nil), server.TokenEndpointAuthMethodsSupport...),
	}
	if err := ValidateOAuthMetadata(ctx, metadata); err != nil {
		return OAuthMetadata{}, fmt.Errorf("authorization server endpoint: %w", err)
	}
	return metadata, nil
}

// probeResourceMetadataURL 试着用一次 initialize 调用，从 server 的
// WWW-Authenticate 挑战头里挖出受保护资源元数据地址。拿不到就返回空串，
// 由调用方回落去 /.well-known/oauth-protected-resource。
func probeResourceMetadataURL(ctx context.Context, endpoint *url.URL) string {
	payload := []byte(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"multica-oauth-discovery","version":"1"}}}`)
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.String(), bytes.NewReader(payload))
	if err != nil {
		return ""
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")
	response, err := NewSecureHTTPClient(endpoint).Do(request)
	if err != nil {
		return ""
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, maxOAuthResponseBytes))
	for _, challenge := range response.Header.Values("WWW-Authenticate") {
		match := resourceMetadataParameter.FindStringSubmatch(challenge)
		if len(match) == 2 {
			return match[1]
		}
	}
	return ""
}

// protectedResourceMetadataURL 按 RFC 9728 的规则把资源地址拼成
// /.well-known/oauth-protected-resource 元数据地址（保留原路径后缀）。
func protectedResourceMetadataURL(resource *url.URL) string {
	result := *resource
	path := strings.TrimSuffix(resource.EscapedPath(), "/")
	if path == "" {
		path = "/"
	}
	if path == "/" {
		result.Path = "/.well-known/oauth-protected-resource"
	} else {
		result.Path = "/.well-known/oauth-protected-resource" + path
	}
	result.RawPath = ""
	result.RawQuery = ""
	result.Fragment = ""
	return result.String()
}

// authorizationMetadataURLs 为授权服务器 issuer 生成两个候选的 well-known
// 元数据地址：标准 RFC 8414 的 oauth-authorization-server，以及 OIDC 的
// openid-configuration（不少厂商只实现后者，作兼容兜底）。
func authorizationMetadataURLs(issuer *url.URL) []string {
	path := strings.TrimSuffix(issuer.EscapedPath(), "/")
	result := make([]string, 0, 2)
	for _, wellKnown := range []string{"oauth-authorization-server", "openid-configuration"} {
		candidate := *issuer
		candidate.Path = "/.well-known/" + wellKnown + path
		candidate.RawPath = ""
		candidate.RawQuery = ""
		candidate.Fragment = ""
		result = append(result, candidate.String())
	}
	return result
}

// validateOAuthURL 校验 OAuth 流程里出现的 URL：必须公网 HTTPS、无 userinfo
// 与 fragment，再去 ValidatePublicHTTPSEndpoint 走一遍 SSRF 检查。query 在
// 检查前被剥离，因为授权类 URL 合法地允许带 query。
func validateOAuthURL(ctx context.Context, raw string) (*url.URL, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Scheme != "https" || parsed.Hostname() == "" || parsed.User != nil || parsed.Fragment != "" {
		return nil, errors.New("URL must be public HTTPS without userinfo or fragment")
	}
	check := *parsed
	check.RawQuery = ""
	if _, err := ValidatePublicHTTPSEndpoint(ctx, check.String(), nil, nil); err != nil {
		return nil, err
	}
	return parsed, nil
}

// getOAuthJSON 用 GET 拉一份 OAuth 元数据文档并反序列化。
func getOAuthJSON(ctx context.Context, endpoint *url.URL, target any) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return err
	}
	request.Header.Set("Accept", "application/json")
	return doOAuthJSON(NewSecureHTTPClient(endpoint), request, target)
}

// doOAuthJSON 发请求、检查状态码与响应大小上限，然后把 JSON 解码进 target。
func doOAuthJSON(client *http.Client, request *http.Request, target any) error {
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, maxOAuthResponseBytes))
		return fmt.Errorf("HTTP %d", response.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, maxOAuthResponseBytes+1))
	if err != nil {
		return err
	}
	if len(raw) > maxOAuthResponseBytes {
		return errors.New("OAuth response exceeds size limit")
	}
	if err := json.Unmarshal(raw, target); err != nil {
		return err
	}
	return nil
}

// RegisterOAuthClient 用 RFC 7591 做动态客户端注册。server 没有注册端点时，
// 用户还可以走「进阶」路径：手工填一个预注册的 client id 来完成连接。
func RegisterOAuthClient(ctx context.Context, metadata OAuthMetadata, redirectURI string) (OAuthClientRegistration, error) {
	if metadata.RegistrationEndpoint == "" {
		return OAuthClientRegistration{}, errors.New("authorization server requires a pre-registered OAuth client")
	}
	endpoint, err := validateOAuthURL(ctx, metadata.RegistrationEndpoint)
	if err != nil {
		return OAuthClientRegistration{}, err
	}
	body, _ := json.Marshal(map[string]any{
		"client_name":                "Multica",
		"redirect_uris":              []string{redirectURI},
		"grant_types":                []string{"authorization_code", "refresh_token"},
		"response_types":             []string{"code"},
		"token_endpoint_auth_method": "none",
	})
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.String(), bytes.NewReader(body))
	if err != nil {
		return OAuthClientRegistration{}, err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	var response struct {
		ClientID                string `json:"client_id"`
		ClientSecret            string `json:"client_secret"`
		TokenEndpointAuthMethod string `json:"token_endpoint_auth_method"`
	}
	if err := doOAuthJSON(NewSecureHTTPClient(endpoint), request, &response); err != nil {
		return OAuthClientRegistration{}, fmt.Errorf("dynamic client registration: %w", err)
	}
	if strings.TrimSpace(response.ClientID) == "" {
		return OAuthClientRegistration{}, errors.New("dynamic client registration returned no client id")
	}
	if response.TokenEndpointAuthMethod == "" {
		response.TokenEndpointAuthMethod = "none"
	}
	return OAuthClientRegistration(response), nil
}

// BuildAuthorizationURL 拼出授权端点的完整 URL：authorization code + PKCE S256
// challenge + state + resource 指示。调用方把用户重定向过去即可。
func BuildAuthorizationURL(metadata OAuthMetadata, registration OAuthClientRegistration, redirectURI, state, verifier, scope string) (string, error) {
	endpoint, err := url.Parse(metadata.AuthorizationEndpoint)
	if err != nil {
		return "", err
	}
	challenge := sha256.Sum256([]byte(verifier))
	query := endpoint.Query()
	query.Set("response_type", "code")
	query.Set("client_id", registration.ClientID)
	query.Set("redirect_uri", redirectURI)
	query.Set("state", state)
	query.Set("code_challenge", base64.RawURLEncoding.EncodeToString(challenge[:]))
	query.Set("code_challenge_method", "S256")
	query.Set("resource", metadata.ResourceEndpoint)
	if strings.TrimSpace(scope) != "" {
		query.Set("scope", strings.TrimSpace(scope))
	}
	endpoint.RawQuery = query.Encode()
	return endpoint.String(), nil
}

// ExchangeOAuthCode 拿授权码换访问令牌（grant_type=authorization_code），
// 附带 PKCE code_verifier 与 resource 指示。
func ExchangeOAuthCode(ctx context.Context, tokenEndpoint, resource, code, redirectURI, verifier string, registration OAuthClientRegistration) (OAuthTokenResponse, error) {
	values := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {redirectURI},
		"client_id":     {registration.ClientID},
		"code_verifier": {verifier},
		"resource":      {resource},
	}
	return requestOAuthToken(ctx, tokenEndpoint, values, registration)
}

// RefreshOAuthToken 用刷新令牌换新的访问令牌（grant_type=refresh_token）。
func RefreshOAuthToken(ctx context.Context, tokenEndpoint, resource, refreshToken string, registration OAuthClientRegistration) (OAuthTokenResponse, error) {
	values := url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {refreshToken},
		"client_id":     {registration.ClientID},
		"resource":      {resource},
	}
	return requestOAuthToken(ctx, tokenEndpoint, values, registration)
}

// requestOAuthToken 是 token 请求的共同骨架：校验端点、按注册时声明的
// token_endpoint_auth_method 带上客户端凭证、POST 表单、解析并校验响应里确实
// 是 Bearer token。expires_in 在响应里可能是数字也可能是字符串，这里两种都兼容。
func requestOAuthToken(ctx context.Context, rawEndpoint string, values url.Values, registration OAuthClientRegistration) (OAuthTokenResponse, error) {
	endpoint, err := validateOAuthURL(ctx, rawEndpoint)
	if err != nil {
		return OAuthTokenResponse{}, err
	}
	if registration.ClientSecret != "" && registration.TokenEndpointAuthMethod == "client_secret_post" {
		values.Set("client_secret", registration.ClientSecret)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.String(), strings.NewReader(values.Encode()))
	if err != nil {
		return OAuthTokenResponse{}, err
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Accept", "application/json")
	if registration.ClientSecret != "" && registration.TokenEndpointAuthMethod == "client_secret_basic" {
		request.SetBasicAuth(registration.ClientID, registration.ClientSecret)
	}
	var response struct {
		AccessToken  string          `json:"access_token"`
		TokenType    string          `json:"token_type"`
		ExpiresIn    json.RawMessage `json:"expires_in"`
		RefreshToken string          `json:"refresh_token"`
		Scope        string          `json:"scope"`
	}
	if err := doOAuthJSON(NewSecureHTTPClient(endpoint), request, &response); err != nil {
		return OAuthTokenResponse{}, err
	}
	if response.AccessToken == "" || !strings.EqualFold(response.TokenType, "Bearer") {
		return OAuthTokenResponse{}, errors.New("token endpoint did not return a Bearer access token")
	}
	expiresIn := int64(0)
	if len(response.ExpiresIn) > 0 {
		if err := json.Unmarshal(response.ExpiresIn, &expiresIn); err != nil {
			var text string
			if json.Unmarshal(response.ExpiresIn, &text) == nil {
				_, _ = fmt.Sscan(text, &expiresIn)
			}
		}
	}
	return OAuthTokenResponse{
		AccessToken: response.AccessToken, TokenType: "Bearer", ExpiresIn: expiresIn,
		RefreshToken: response.RefreshToken, Scope: response.Scope,
	}, nil
}

// OAuthExpiry 把 expires_in（秒）换算成绝对过期时间。非正值（未给/异常）
// 返回零值 time.Time，表示「不知道何时过期」。
func OAuthExpiry(now time.Time, expiresIn int64) time.Time {
	if expiresIn <= 0 {
		return time.Time{}
	}
	return now.Add(time.Duration(expiresIn) * time.Second)
}
