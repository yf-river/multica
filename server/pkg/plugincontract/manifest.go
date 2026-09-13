// Package plugincontract 定义插件作者与 Multica host 之间带版本的公共契约。
// 它必须与私有 handler、service 和数据库实现保持独立。
//
// 插件与 Multica 的关系恰好有三种：
//
//   - Action   (plugin -> Multica)：插件调用 host 的能力。
//   - Hook     (Multica -> plugin)：host 调用插件的能力。
//   - Resource (双向都不调用)：    静态贡献，如 skill 文本。
//
// 「谁触发」与「调用什么能力」是正交的：hook 只声明一次，可被它选择加入的
// 任意 trigger 调用。
package plugincontract

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/robfig/cron/v3"
)

const (
	// ManifestVersion1 是本 host 认识的唯一 manifest 版本。
	ManifestVersion1 = 1

	// ManifestFilename 是插件包内约定的清单文件名。
	ManifestFilename = "multica.plugin.json"

	// MaxManifestSize 限制拉取到的 manifest 在解析前的字节数。
	MaxManifestSize = 1 << 20

	// MaxVersionLength 对齐 plugin_installation.version 列的宽度上限。
	MaxVersionLength = 64
)

// Surface 类型。surface 是 host 在固定位置挂载的一个 iframe。
const (
	SurfaceIssuePanel   = "issue_panel"
	SurfaceSidebarPanel = "sidebar_panel"
	SurfaceModal        = "modal"
)

// Hook 的 trigger。声明 trigger 说的是「谁可以调用这个 hook」，而不是 hook 干
// 什么。`event` 与 `schedule` 是异步的，都不会阻塞面向用户的 host 请求。
const (
	TriggerUI       = "ui"
	TriggerManual   = "manual"
	TriggerAgent    = "agent"
	TriggerEvent    = "event"
	TriggerSchedule = "schedule"
)

// 定时 hook 是一种轮询原语，不是高频定时器。这个下限既能防止某个安装把它的
// manifest 变成无限出站流量的来源，也留出了 HTTP 超时加可靠重试的空间，
// 不至于下一次执行已经到期。
const MinimumScheduleInterval = 5 * time.Minute

// scheduleCronParser 只接受标准 5 段 cron（分/时/日/月/周），拒绝秒级写法。
var scheduleCronParser = cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)

// Hook 的传输方式。
const (
	TransportHTTP = "http"
	TransportMCP  = "mcp"
)

// Resource 类型。resource 两个方向都不产生调用。
const (
	ResourceSkill = "skill"
)

// 配置字段类型。这是 JSON Schema 的一个刻意很小的子集：host 要根据它渲染配置
// 表单，所以插件永远不需要自带表单 UI。
const (
	ConfigString = "string"
	ConfigNumber = "number"
	ConfigBool   = "bool"
	ConfigEnum   = "enum"
	ConfigSecret = "secret"
)

// Scopes。这份清单完整且封闭；其它任何值都会在解析期被拒绝。
// `net:<domain>` 是唯一带参数的形式。
const (
	ScopeIssuesRead       = "issues:read"
	ScopeIssuesWrite      = "issues:write"
	ScopeCommentsRead     = "comments:read"
	ScopeCommentsWrite    = "comments:write"
	ScopeTasksRead        = "tasks:read"
	ScopeTasksWrite       = "tasks:write"
	ScopeAgentsRead       = "agents:read"
	ScopeMembersRead      = "members:read"
	ScopeStorageUser      = "storage:user"
	ScopeStorageWorkspace = "storage:workspace"

	// ScopeNetPrefix 管辖插件出站网络：iframe 的 CSP connect-src 白名单与
	// hook 传输层的 host 检查都从它推导。
	ScopeNetPrefix = "net:"
)

// 一个以 `event` 触发的 hook 可以订阅的产品事件。
const (
	EventIssueCreated       = "issue.created"
	EventIssueUpdated       = "issue.updated"
	EventIssueStatusChanged = "issue.status_changed"
	EventCommentCreated     = "comment.created"
	EventTaskStarted        = "task.started"
	EventTaskCompleted      = "task.completed"
	EventTaskFailed         = "task.failed"
)

// fixedScopes 是不带参数的全部固定 scope。
var fixedScopes = map[string]bool{
	ScopeIssuesRead:       true,
	ScopeIssuesWrite:      true,
	ScopeCommentsRead:     true,
	ScopeCommentsWrite:    true,
	ScopeTasksRead:        true,
	ScopeTasksWrite:       true,
	ScopeAgentsRead:       true,
	ScopeMembersRead:      true,
	ScopeStorageUser:      true,
	ScopeStorageWorkspace: true,
}

func hasScope(scopes []string, want string) bool {
	for _, scope := range scopes {
		if scope == want {
			return true
		}
	}
	return false
}

// eventReadScope 把每个事件映射到「想通过 Action API 读同一份内容所需的 scope」。
var eventReadScope = map[string]string{
	EventIssueCreated:       ScopeIssuesRead,
	EventIssueUpdated:       ScopeIssuesRead,
	EventIssueStatusChanged: ScopeIssuesRead,
	EventCommentCreated:     ScopeCommentsRead,
	EventTaskStarted:        ScopeTasksRead,
	EventTaskCompleted:      ScopeTasksRead,
	EventTaskFailed:         ScopeTasksRead,
}

var knownEvents = map[string]bool{
	EventIssueCreated:       true,
	EventIssueUpdated:       true,
	EventIssueStatusChanged: true,
	EventCommentCreated:     true,
	EventTaskStarted:        true,
	EventTaskCompleted:      true,
	EventTaskFailed:         true,
}

// IsKnownEvent 报告某个事件是否可被 manifest 订阅。导出它，是为了让分发器
// 不可能发布一个任何插件都收不到的事件——两份清单必须一致，而其中只有一份
// 是权威的。
func IsKnownEvent(event string) bool { return knownEvents[event] }

// 校验用正则。pluginKeySegmentPattern 允许小写字母数字加连字符的反向 DNS 段；
// contributionKeyPattern 允许带连字符/下划线的贡献 key；semverPattern 是
// 宽松版 semver；netDomainPattern 是普通域名；relativePathPattern 是不含协议、
// 前导斜杠与越界的相对路径。
var (
	pluginKeySegmentPattern = regexp.MustCompile(`^[a-z][a-z0-9]*(?:-[a-z0-9]+)*$`)
	contributionKeyPattern  = regexp.MustCompile(`^[a-z][a-z0-9]*(?:[_-][a-z0-9]+)*$`)
	semverPattern           = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?(?:\+[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?$`)
	netDomainPattern        = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)+$`)
	relativePathPattern     = regexp.MustCompile(`^[A-Za-z0-9._-]+(?:/[A-Za-z0-9._-]+)*$`)
)

// Manifest 是插件的顶层清单：身份、作者、scope 申请、配置 schema 与贡献清单。
type Manifest struct {
	ManifestVersion int          `json:"manifest_version"`
	Key             string       `json:"key"`
	Name            string       `json:"name"`
	Description     string       `json:"description,omitempty"`
	Version         string       `json:"version"`
	Author          Author       `json:"author"`
	Icon            string       `json:"icon,omitempty"`
	Scopes          []string     `json:"scopes"`
	Config          ConfigSchema `json:"config,omitempty"`
	Contributes     Contributes  `json:"contributes"`
}

// Author 是插件作者署名。
type Author struct {
	Name string `json:"name"`
	URL  string `json:"url,omitempty"`
}

// Contributes 汇总插件贡献的三类东西：surfaces、hooks、resources。
type Contributes struct {
	Surfaces  []Surface  `json:"surfaces,omitempty"`
	Hooks     []Hook     `json:"hooks,omitempty"`
	Resources []Resource `json:"resources,omitempty"`
}

// Surface 是一个由 host 挂载的 iframe。出现在哪由 host 决定；里面渲染什么由
// 插件决定。
type Surface struct {
	Key       string   `json:"key"`
	Type      string   `json:"type"`
	Name      string   `json:"name"`
	Entry     string   `json:"entry"`
	Platforms []string `json:"platforms,omitempty"`
}

// Hook 是插件侧的一项能力。Triggers 说明谁可以调用它；host 绝不会发明一个
// 不在此列出的调用点。
type Hook struct {
	Key         string          `json:"key"`
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema,omitempty"`
	Triggers    []string        `json:"triggers"`
	Events      []string        `json:"events,omitempty"`
	Schedule    *HookSchedule   `json:"schedule,omitempty"`
	Transport   HookTransport   `json:"transport"`
	TimeoutMs   int             `json:"timeout_ms,omitempty"`
}

// HookSchedule 声明挂到某个 hook 上的唯一一个自动节奏。多个节奏用多个 hook
// key 表示——这样持久化执行身份保持无歧义。
type HookSchedule struct {
	Cron     string `json:"cron"`
	Timezone string `json:"timezone"`
}

// HookTransport 描述 hook 怎么被调用：类型（http/mcp）与目标 URL。
type HookTransport struct {
	Type string `json:"type"`
	URL  string `json:"url"`
}

// Resource 是一次静态贡献。两个方向都没有调用发生，所以 resource 不是 hook。
type Resource struct {
	Type  string `json:"type"`
	Key   string `json:"key"`
	Entry string `json:"entry"`
}

// ConfigField 是一个由 host 渲染的配置输入。Secret 只写不读，
// 任何读接口都不会把它回显。
type ConfigField struct {
	Key         string   `json:"-"`
	Type        string   `json:"type"`
	Label       string   `json:"label"`
	Description string   `json:"description,omitempty"`
	Required    bool     `json:"required,omitempty"`
	Options     []string `json:"options,omitempty"`
	Placeholder string   `json:"placeholder,omitempty"`
	// Multiline 请 host 渲染成 textarea。只对 string 字段有意义；没有它，
	// 一个内容是「多行列表」的值在生成的表单里根本没法读——而那正是 host
	// 负责渲染的唯一一件事。
	Multiline bool `json:"multiline,omitempty"`
}

// ConfigSchema 保留声明顺序，让生成的表单在多次安装间保持稳定。
// 线格式仍是「按字段名作键」的 JSON 对象。
type ConfigSchema struct {
	Fields []ConfigField
}

// Len 返回配置字段数量。
func (c ConfigSchema) Len() int { return len(c.Fields) }

// Field 按键名查配置字段。
func (c ConfigSchema) Field(key string) (ConfigField, bool) {
	for _, field := range c.Fields {
		if field.Key == key {
			return field, true
		}
	}
	return ConfigField{}, false
}

// MarshalJSON 把 ConfigSchema 序列化成按声明顺序排列的 JSON 对象。
func (c ConfigSchema) MarshalJSON() ([]byte, error) {
	if len(c.Fields) == 0 {
		return []byte("{}"), nil
	}
	var buf bytes.Buffer
	buf.WriteByte('{')
	for i, field := range c.Fields {
		if i > 0 {
			buf.WriteByte(',')
		}
		key, err := json.Marshal(field.Key)
		if err != nil {
			return nil, err
		}
		buf.Write(key)
		buf.WriteByte(':')
		value, err := json.Marshal(struct {
			Type        string   `json:"type"`
			Label       string   `json:"label"`
			Description string   `json:"description,omitempty"`
			Required    bool     `json:"required,omitempty"`
			Options     []string `json:"options,omitempty"`
			Placeholder string   `json:"placeholder,omitempty"`
			Multiline   bool     `json:"multiline,omitempty"`
		}{field.Type, field.Label, field.Description, field.Required, field.Options, field.Placeholder, field.Multiline})
		if err != nil {
			return nil, err
		}
		buf.Write(value)
	}
	buf.WriteByte('}')
	return buf.Bytes(), nil
}

// UnmarshalJSON 把 JSON 对象解回 ConfigSchema：字段名作 key、保留对象内的
// 出现顺序、拒绝重复字段与未知子字段。
func (c *ConfigSchema) UnmarshalJSON(raw []byte) error {
	c.Fields = nil
	decoder := json.NewDecoder(bytes.NewReader(raw))
	token, err := decoder.Token()
	if err != nil {
		return fmt.Errorf("config must be a JSON object: %w", err)
	}
	if delim, ok := token.(json.Delim); !ok || delim != '{' {
		return fmt.Errorf("config must be a JSON object")
	}
	seen := map[string]bool{}
	for decoder.More() {
		keyToken, err := decoder.Token()
		if err != nil {
			return fmt.Errorf("config field name: %w", err)
		}
		key, ok := keyToken.(string)
		if !ok {
			return fmt.Errorf("config field name must be a string")
		}
		if seen[key] {
			return fmt.Errorf("config contains duplicate field %q", key)
		}
		seen[key] = true

		var body json.RawMessage
		if err := decoder.Decode(&body); err != nil {
			return fmt.Errorf("config.%s: %w", key, err)
		}
		fieldDecoder := json.NewDecoder(bytes.NewReader(body))
		fieldDecoder.DisallowUnknownFields()
		var field ConfigField
		if err := fieldDecoder.Decode(&field); err != nil {
			return fmt.Errorf("config.%s: %w", key, err)
		}
		field.Key = key
		c.Fields = append(c.Fields, field)
	}
	if _, err := decoder.Token(); err != nil {
		return fmt.Errorf("config object: %w", err)
	}
	return nil
}

// ParseManifest 严格解码 v1 manifest，并返回安装时作为「已同意快照」存储的
// 规范化字节。未知字段直接报错而不是忽略——这样一个拼写错误不可能悄悄削弱
// 管理员批准过的东西。
func ParseManifest(raw []byte) (Manifest, []byte, error) {
	if len(raw) == 0 {
		return Manifest{}, nil, fmt.Errorf("plugin manifest is empty")
	}
	if len(raw) > MaxManifestSize {
		return Manifest{}, nil, fmt.Errorf("plugin manifest exceeds %d bytes", MaxManifestSize)
	}

	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()

	var manifest Manifest
	if err := decoder.Decode(&manifest); err != nil {
		return Manifest{}, nil, fmt.Errorf("decode plugin manifest: %w", err)
	}
	if err := rejectTrailingJSON(decoder); err != nil {
		return Manifest{}, nil, err
	}
	if err := manifest.Validate(); err != nil {
		return Manifest{}, nil, err
	}

	canonical, err := json.Marshal(manifest)
	if err != nil {
		return Manifest{}, nil, fmt.Errorf("canonicalize plugin manifest: %w", err)
	}
	return manifest, canonical, nil
}

// rejectTrailingJSON 确保主文档之后没有任何多余 JSON 内容。
func rejectTrailingJSON(decoder *json.Decoder) error {
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return fmt.Errorf("plugin manifest contains trailing JSON")
		}
		return fmt.Errorf("decode trailing plugin manifest data: %w", err)
	}
	return nil
}

// Validate 对一份 Manifest 做完整语义校验（结构已在解码期保证）。
func (m Manifest) Validate() error {
	if m.ManifestVersion != ManifestVersion1 {
		return fmt.Errorf("manifest_version must be %d", ManifestVersion1)
	}
	if err := validatePluginKey(m.Key); err != nil {
		return err
	}
	if err := validateDisplayText("name", m.Name, 160); err != nil {
		return err
	}
	if len(m.Description) > 2000 {
		return fmt.Errorf("description exceeds 2000 bytes")
	}
	if strings.ContainsAny(m.Description, "\r") {
		return fmt.Errorf("description must not contain carriage returns")
	}
	// 先卡长度再相信正则结果：semver 允许无界的构建/预发布段，而
	// plugin_installation.version 上限是 64。在这里拒绝，把 INSERT 时刻的约束
	// 违例变成指名道姓字段的解析错误。
	if len(m.Version) > MaxVersionLength {
		return fmt.Errorf("version exceeds %d bytes", MaxVersionLength)
	}
	if !semverPattern.MatchString(m.Version) {
		return fmt.Errorf("version must be semantic versioning, got %q", m.Version)
	}
	if err := validateDisplayText("author.name", m.Author.Name, 160); err != nil {
		return err
	}
	if m.Author.URL != "" {
		if err := validateHTTPSURL("author.url", m.Author.URL); err != nil {
			return err
		}
	}
	if m.Icon != "" {
		if err := validateRelativePath("icon", m.Icon); err != nil {
			return err
		}
	}
	if err := m.validateScopes(); err != nil {
		return err
	}
	if err := m.validateConfig(); err != nil {
		return err
	}
	return m.validateContributions()
}

// validateScopes 校验 scope 列表：非空、有上限、无重复、逐项合法。
func (m Manifest) validateScopes() error {
	if len(m.Scopes) == 0 {
		return fmt.Errorf("scopes must not be empty")
	}
	if len(m.Scopes) > 64 {
		return fmt.Errorf("scopes must not exceed 64 entries")
	}
	seen := map[string]bool{}
	for index, scope := range m.Scopes {
		if seen[scope] {
			return fmt.Errorf("scopes contains duplicate value %q", scope)
		}
		seen[scope] = true
		if err := ValidateScope(scope); err != nil {
			return fmt.Errorf("scopes[%d]: %w", index, err)
		}
	}
	return nil
}

// ValidateScope 报告单个 scope 字符串是否属于本 host 定义的范围。
func ValidateScope(scope string) error {
	if fixedScopes[scope] {
		return nil
	}
	if domain, ok := strings.CutPrefix(scope, ScopeNetPrefix); ok {
		if len(domain) > 253 || !netDomainPattern.MatchString(domain) {
			return fmt.Errorf("net scope has an invalid domain %q", domain)
		}
		return nil
	}
	return fmt.Errorf("unsupported scope %q", scope)
}

// NetDomains 返回某个安装可以触达的域名，只取自它被授予的 scope。
func NetDomains(scopes []string) []string {
	domains := make([]string, 0, len(scopes))
	for _, scope := range scopes {
		if domain, ok := strings.CutPrefix(scope, ScopeNetPrefix); ok {
			domains = append(domains, domain)
		}
	}
	return domains
}

// validateConfig 校验配置 schema 的字段：个数、命名、类型专属约束。
func (m Manifest) validateConfig() error {
	if m.Config.Len() > 32 {
		return fmt.Errorf("config must not exceed 32 fields")
	}
	for _, field := range m.Config.Fields {
		if !contributionKeyPattern.MatchString(field.Key) {
			return fmt.Errorf("config contains invalid field name %q", field.Key)
		}
		label := "config." + field.Key
		switch field.Type {
		case ConfigString, ConfigNumber, ConfigBool, ConfigSecret:
			if len(field.Options) > 0 {
				return fmt.Errorf("%s.options is only valid for enum fields", label)
			}
			if field.Multiline && field.Type != ConfigString {
				return fmt.Errorf("%s.multiline is only valid for string fields", label)
			}
		case ConfigEnum:
			if len(field.Options) == 0 {
				return fmt.Errorf("%s.options must not be empty for enum fields", label)
			}
			if len(field.Options) > 64 {
				return fmt.Errorf("%s.options must not exceed 64 entries", label)
			}
			optionSeen := map[string]bool{}
			for _, option := range field.Options {
				if err := validateDisplayText(label+".options", option, 160); err != nil {
					return err
				}
				if optionSeen[option] {
					return fmt.Errorf("%s.options contains duplicate value %q", label, option)
				}
				optionSeen[option] = true
			}
		default:
			return fmt.Errorf("%s.type is unsupported: %q", label, field.Type)
		}
		if field.Multiline && field.Type != ConfigString {
			return fmt.Errorf("%s.multiline is only valid for string fields", label)
		}
		if err := validateDisplayText(label+".label", field.Label, 160); err != nil {
			return err
		}
		if len(field.Description) > 500 {
			return fmt.Errorf("%s.description exceeds 500 bytes", label)
		}
		if len(field.Placeholder) > 160 {
			return fmt.Errorf("%s.placeholder exceeds 160 bytes", label)
		}
	}
	return nil
}

// validateContributions 校验贡献清单：至少一项、总量上限、key 唯一、各 surface/
// hook/resource 的类型与字段逐一合法。
func (m Manifest) validateContributions() error {
	total := len(m.Contributes.Surfaces) + len(m.Contributes.Hooks) + len(m.Contributes.Resources)
	if total == 0 {
		return fmt.Errorf("contributes must declare at least one surface, hook, or resource")
	}
	if total > 64 {
		return fmt.Errorf("contributes must not exceed 64 entries")
	}

	surfaceKeys := map[string]bool{}
	for index, surface := range m.Contributes.Surfaces {
		field := fmt.Sprintf("contributes.surfaces[%d]", index)
		if !contributionKeyPattern.MatchString(surface.Key) {
			return fmt.Errorf("%s.key is invalid", field)
		}
		if surfaceKeys[surface.Key] {
			return fmt.Errorf("duplicate surface key %q", surface.Key)
		}
		surfaceKeys[surface.Key] = true
		switch surface.Type {
		case SurfaceIssuePanel, SurfaceSidebarPanel, SurfaceModal:
		default:
			return fmt.Errorf("%s.type is unsupported: %q", field, surface.Type)
		}
		if err := validateDisplayText(field+".name", surface.Name, 160); err != nil {
			return err
		}
		if err := validateRelativePath(field+".entry", surface.Entry); err != nil {
			return err
		}
		// host 自己生成 surface 的 HTML 文档，并把这段脚本加载进去。这正是
		// host 能给文档挂上「由 manifest 的 net: scope 推导出的 CSP」的前提——
		// 若是插件自带 HTML，策略就随它自己的 server 走了，net: 会从「控制」
		// 变成一句声明。
		if !strings.HasSuffix(surface.Entry, ".js") && !strings.HasSuffix(surface.Entry, ".mjs") {
			return fmt.Errorf("%s.entry must be a .js or .mjs script; the host renders the surface document itself", field)
		}
		platformSeen := map[string]bool{}
		for _, platform := range surface.Platforms {
			if platform != "web" && platform != "desktop" {
				return fmt.Errorf("%s.platforms contains unsupported platform %q", field, platform)
			}
			if platformSeen[platform] {
				return fmt.Errorf("%s.platforms contains duplicate platform %q", field, platform)
			}
			platformSeen[platform] = true
		}
	}

	hookKeys := map[string]bool{}
	for index, hook := range m.Contributes.Hooks {
		field := fmt.Sprintf("contributes.hooks[%d]", index)
		if !contributionKeyPattern.MatchString(hook.Key) {
			return fmt.Errorf("%s.key is invalid", field)
		}
		if hookKeys[hook.Key] {
			return fmt.Errorf("duplicate hook key %q", hook.Key)
		}
		hookKeys[hook.Key] = true
		if err := validateDisplayText(field+".name", hook.Name, 160); err != nil {
			return err
		}
		// description 是 agent 会当作 MCP 工具描述来读的内容，所以它必填、
		// 且允许跨多行。
		if strings.TrimSpace(hook.Description) == "" || len(hook.Description) > 2000 {
			return fmt.Errorf("%s.description must be non-empty and at most 2000 bytes", field)
		}
		if len(hook.InputSchema) > 0 {
			var schema map[string]any
			if err := json.Unmarshal(hook.InputSchema, &schema); err != nil {
				return fmt.Errorf("%s.input_schema must be a JSON object: %w", field, err)
			}
			if schemaType, _ := schema["type"].(string); schemaType != "object" {
				return fmt.Errorf("%s.input_schema.type must be object", field)
			}
		}
		if len(hook.Triggers) == 0 {
			return fmt.Errorf("%s.triggers must not be empty", field)
		}
		triggerSeen := map[string]bool{}
		for _, trigger := range hook.Triggers {
			switch trigger {
			case TriggerUI, TriggerManual, TriggerAgent, TriggerEvent, TriggerSchedule:
			default:
				return fmt.Errorf("%s.triggers contains unsupported trigger %q", field, trigger)
			}
			if triggerSeen[trigger] {
				return fmt.Errorf("%s.triggers contains duplicate trigger %q", field, trigger)
			}
			triggerSeen[trigger] = true
		}
		if triggerSeen[TriggerEvent] {
			if len(hook.Events) == 0 {
				return fmt.Errorf("%s.events must not be empty when the event trigger is declared", field)
			}
			eventSeen := map[string]bool{}
			for _, event := range hook.Events {
				if !knownEvents[event] {
					return fmt.Errorf("%s.events contains unsupported event %q", field, event)
				}
				if eventSeen[event] {
					return fmt.Errorf("%s.events contains duplicate event %q", field, event)
				}
				eventSeen[event] = true
				// 事件推送的内容，正是 Action API 需要 scope 才能拉到的那份：
				// issue.* 带 description，comment.created 带正文。没有这条，
				// 订阅就成了「没被授予读权也能收到同一份数据」——同一份数据
				// 两套标准。安装期就强制，让同意页面上展示的读 scope 与订阅
				// 实际隐含的一致。
				if required := eventReadScope[event]; required != "" && !hasScope(m.Scopes, required) {
					return fmt.Errorf("%s.events subscribes to %q, which delivers content requiring the %s scope", field, event, required)
				}
			}
		} else if len(hook.Events) > 0 {
			return fmt.Errorf("%s.events requires the event trigger", field)
		}
		if triggerSeen[TriggerSchedule] {
			if hook.Schedule == nil {
				return fmt.Errorf("%s.schedule is required when the schedule trigger is declared", field)
			}
			if hook.Transport.Type != TransportHTTP {
				return fmt.Errorf("%s.schedule only supports the http transport", field)
			}
			if err := validateHookSchedule(field+".schedule", *hook.Schedule); err != nil {
				return err
			}
		} else if hook.Schedule != nil {
			return fmt.Errorf("%s.schedule requires the schedule trigger", field)
		}
		if err := m.validateHookTransport(field, hook.Transport); err != nil {
			return err
		}
		if hook.TimeoutMs != 0 && (hook.TimeoutMs < 100 || hook.TimeoutMs > 30000) {
			return fmt.Errorf("%s.timeout_ms must be between 100 and 30000", field)
		}
	}

	resourceKeys := map[string]bool{}
	for index, resource := range m.Contributes.Resources {
		field := fmt.Sprintf("contributes.resources[%d]", index)
		if resource.Type != ResourceSkill {
			return fmt.Errorf("%s.type is unsupported: %q", field, resource.Type)
		}
		if !contributionKeyPattern.MatchString(resource.Key) {
			return fmt.Errorf("%s.key is invalid", field)
		}
		if resourceKeys[resource.Key] {
			return fmt.Errorf("duplicate resource key %q", resource.Key)
		}
		resourceKeys[resource.Key] = true
		if err := validateRelativePath(field+".entry", resource.Entry); err != nil {
			return err
		}
		if want := "skills/" + resource.Key + "/SKILL.md"; resource.Entry != want {
			return fmt.Errorf("%s.entry must be %q", field, want)
		}
	}
	return nil
}

// validateHookSchedule 校验定时 hook 的 cron 与时区，并保证执行间隔不低于
// MinimumScheduleInterval。
func validateHookSchedule(field string, schedule HookSchedule) error {
	expression := strings.TrimSpace(schedule.Cron)
	if expression == "" {
		return fmt.Errorf("%s.cron must not be empty", field)
	}
	// 时区放在它自己的字段里。除了让公共形状保持单一，拒绝行内前缀还能避开
	// robfig/cron 对畸形前缀的 panic 路径，并防止两处时区声明互相矛盾。
	if strings.HasPrefix(expression, "TZ=") || strings.HasPrefix(expression, "CRON_TZ=") {
		return fmt.Errorf("%s.cron must not contain an inline timezone", field)
	}
	parsed, err := scheduleCronParser.Parse(expression)
	if err != nil {
		return fmt.Errorf("%s.cron must be a standard five-field cron expression: %w", field, err)
	}
	if strings.TrimSpace(schedule.Timezone) == "" {
		return fmt.Errorf("%s.timezone must not be empty", field)
	}
	location, err := time.LoadLocation(schedule.Timezone)
	if err != nil {
		return fmt.Errorf("%s.timezone is invalid: %w", field, err)
	}

	// 400 天的窗口覆盖所有月份加一个 DST 周期。高频率表达式在前两次出现后
	// 就会失败；合规的五分钟表达式保持在 11.6 万次迭代以内——代价付在发布/
	// 预览时刻，而不是每个调度 tick 上。月/年表达式在窗口内零或一次出现是
	// 合法定义。
	start := time.Date(2024, time.January, 1, 0, 0, 0, 0, time.UTC).In(location)
	end := start.AddDate(0, 0, 400)
	previous := parsed.Next(start)
	for !previous.IsZero() && !previous.After(end) {
		next := parsed.Next(previous)
		if next.IsZero() || next.After(end) {
			break
		}
		if next.Sub(previous) < MinimumScheduleInterval {
			return fmt.Errorf("%s.cron must not run more often than every five minutes", field)
		}
		previous = next
	}
	return nil
}

// validateHookTransport 强制 hook 只能触达 manifest 通过 `net:` scope 声明过的
// host。没有这条，同意页面就描述不了插件数据实际流向哪里。
func (m Manifest) validateHookTransport(field string, transport HookTransport) error {
	switch transport.Type {
	case TransportHTTP, TransportMCP:
	default:
		return fmt.Errorf("%s.transport.type is unsupported: %q", field, transport.Type)
	}
	if err := validateHTTPSURL(field+".transport.url", transport.URL); err != nil {
		return err
	}
	endpoint, err := url.Parse(transport.URL)
	if err != nil {
		return fmt.Errorf("%s.transport.url is invalid", field)
	}
	// 精确匹配 host，绝不用后缀匹配。同意页面每个 scope 渲染一行
	// （"send data to example.com"），同一份 scope 列表又成为 iframe 的 CSP
	// connect-src，而 CSP 是精确 host 匹配。这里若用后缀匹配，同一个 scope
	// 字符串在两处会变成两种意思。需要子域的插件自己声明：net:api.example.com。
	host := strings.ToLower(strings.TrimSuffix(endpoint.Hostname(), "."))
	for _, domain := range NetDomains(m.Scopes) {
		if host == domain {
			return nil
		}
	}
	return fmt.Errorf("%s.transport.url host %q is not covered by a net: scope", field, host)
}

// validatePluginKey 校验插件 key：反 DNS 命名空间、段数 ≥2、每段合法。
func validatePluginKey(key string) error {
	if len(key) > 255 {
		return fmt.Errorf("key exceeds 255 bytes")
	}
	segments := strings.Split(key, ".")
	if len(segments) < 2 {
		return fmt.Errorf("key must use a reverse-DNS namespace")
	}
	for _, segment := range segments {
		if !pluginKeySegmentPattern.MatchString(segment) {
			return fmt.Errorf("key contains invalid segment %q", segment)
		}
	}
	return nil
}

// validateDisplayText 校验一段给人看的短文本：非空、无首尾空白、单行、长度受限。
func validateDisplayText(field, value string, maxBytes int) error {
	if value == "" || value != strings.TrimSpace(value) {
		return fmt.Errorf("%s must be non-empty without surrounding whitespace", field)
	}
	if strings.ContainsAny(value, "\r\n") {
		return fmt.Errorf("%s must be single-line", field)
	}
	if len(value) > maxBytes {
		return fmt.Errorf("%s exceeds %d bytes", field, maxBytes)
	}
	return nil
}

// validateRelativePath 校验相对路径：非空、长度受限、无协议/前导斜杠/越界。
func validateRelativePath(field, value string) error {
	if value == "" || len(value) > 1024 {
		return fmt.Errorf("%s must be a relative path of at most 1024 bytes", field)
	}
	if !relativePathPattern.MatchString(value) {
		return fmt.Errorf("%s must be a relative path without protocol, leading slash, or traversal", field)
	}
	for _, segment := range strings.Split(value, "/") {
		if segment == "." || segment == ".." {
			return fmt.Errorf("%s must not contain path traversal", field)
		}
	}
	return nil
}

// validateHTTPSURL 校验一个纯 HTTPS URL：长度受限、https 协议、无 userinfo
// 与 fragment。
func validateHTTPSURL(field, value string) error {
	if len(value) > 2048 {
		return fmt.Errorf("%s exceeds 2048 bytes", field)
	}
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil || parsed.Scheme != "https" || parsed.Hostname() == "" || parsed.User != nil || parsed.Fragment != "" {
		return fmt.Errorf("%s must be a plain HTTPS URL", field)
	}
	return nil
}
