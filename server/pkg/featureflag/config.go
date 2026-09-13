package featureflag

// 本文件职责：把功能开关从「配置文件/环境」装配起来——定义 YAML 规则文件的
// 磁盘线格式与解析器，以及一键从环境变量构造标准 provider 链的 NewServiceFromEnv。

import (
	"fmt"
	"log/slog"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// EnvFlagFile 是 NewServiceFromEnv 用来定位 YAML 规则文件所读的环境变量名。
// 它沿用 .env.example 里记录的其它 MULTICA_*_CONFIG / MULTICA_*_FILE 开关的
// 命名约定。
const EnvFlagFile = "MULTICA_FEATURE_FLAGS_FILE"

// EnvOverridePrefix 是 EnvProvider 用的前缀——NewServiceFromEnv 组合标准
// provider 链时，环境变量层在最前面。运行时可以用 `FF_<FLAG_KEY>=true|false|
// 42%|<variant>` 覆盖单个开关，而不用碰 YAML 文件——环境变量覆盖压过文件值。
const EnvOverridePrefix = "FF_"

// ruleConfig 是 YAML / JSON 加载器用的线格式。它镜像 Rule，但用 snake_case
// 键（YAML 写起来顺手）和指针类型（能区分「没填」与「显式填了零值」）。
//
// 让线格式与运行时的 Rule 分离，意味着配置格式可以演进（加字段、废弃旧名）
// 而不必逼着所有用 Rule 的业务代码重新编译适配新形状。
type ruleConfig struct {
	Default *bool          `yaml:"default,omitempty"`
	Variant string         `yaml:"variant,omitempty"`
	Allow   []string       `yaml:"allow,omitempty"`
	AllowBy string         `yaml:"allow_by,omitempty"`
	Deny    []string       `yaml:"deny,omitempty"`
	DenyBy  string         `yaml:"deny_by,omitempty"`
	Percent *percentConfig `yaml:"percent,omitempty"`
}

// percentConfig 是规则里 percent 块的线格式。
type percentConfig struct {
	Percent int    `yaml:"percent"`
	By      string `yaml:"by,omitempty"`
}

// toRule 把线格式转成运行时 Rule，给 YAML 里没填的字段套默认值。
func (rc ruleConfig) toRule() Rule {
	r := Rule{
		Variant: rc.Variant,
		Allow:   rc.Allow,
		AllowBy: rc.AllowBy,
		Deny:    rc.Deny,
		DenyBy:  rc.DenyBy,
	}
	if rc.Default != nil {
		r.Default = *rc.Default
	}
	if rc.Percent != nil {
		r.Percent = &PercentRollout{
			Percent: rc.Percent.Percent,
			By:      rc.Percent.By,
		}
	}
	return r
}

// LoadRulesFromYAMLFile 读取一个把开关 key 映射到规则定义的 YAML 文件，
// 返回解析好的 map，可直接用 LoadRules 装到 StaticProvider 上。
//
// Schema（除 `default` 外所有字段都可选）：
//
//	billing_new_invoice_email:
//	  default: true
//
//	checkout_algo:
//	  default: false
//	  variant: experiment-v2
//	  percent:
//	    percent: 25
//	    by: user_id
//
//	ops_disable_recommendations:
//	  default: false
//	  allow: ["user-internal-1", "user-internal-2"]
//
// 空文件（或只有空白）返回空 map 且不报错，这样运维可以先放一个开关文件在
// 位，等真正写好开关再填内容，服务器启动不会因此挂掉。
func LoadRulesFromYAMLFile(path string) (map[string]Rule, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("featureflag: read %s: %w", path, err)
	}
	return parseRulesYAML(data)
}

// parseRulesYAML 是 LoadRulesFromYAMLFile 里懂文件格式的核心逻辑。
// 保持未导出，方便测试直接测解析器而不碰文件系统。
func parseRulesYAML(data []byte) (map[string]Rule, error) {
	// 空 body 是合法的「还没定义任何开关」状态。yaml.Unmarshal 处理 `nil`
	// 时目标保持 nil，所以这里显式兜一手，给调用方返回一个空（非 nil）map。
	if len(strings.TrimSpace(string(data))) == 0 {
		return map[string]Rule{}, nil
	}
	var raw map[string]ruleConfig
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("featureflag: parse: %w", err)
	}
	out := make(map[string]Rule, len(raw))
	for key, rc := range raw {
		out[key] = rc.toRule()
	}
	return out, nil
}

// NewServiceFromEnv 按 multica 标准配置来源装配一个 Service，
// 优先级从高到低为：
//
//  1. EnvProvider（FF_<KEY> 覆盖——Ops 急停开关，最快的通路）。
//  2. 从 MULTICA_FEATURE_FLAGS_FILE 指向的 YAML 文件加载的 StaticProvider
//     （仅当环境变量已设置且文件存在时）。
//
// MULTICA_FEATURE_FLAGS_FILE 未设置时 Service 照常工作——EnvProvider 是
// 唯一的一层，任何没有 FF_<KEY> 覆盖的开关，IsEnabled 都会回落到调用方的
// 默认值。因此服务器在任何开关配置被写出来之前就能启动。
//
// 文件路径设置了但文件格式非法时，这里返回错误而不是默默丢弃配置——
// 运维期望功能开关配错时像其它所有配置旋钮一样大声失败
// （DATABASE_URL 解析错误、生产环境缺 JWT_SECRET 等）。
func NewServiceFromEnv(opts ...Option) (*Service, error) {
	var providers []Provider
	providers = append(providers, NewEnvProvider(EnvOverridePrefix))

	path := strings.TrimSpace(os.Getenv(EnvFlagFile))
	var loadedCount int
	if path != "" {
		rules, err := LoadRulesFromYAMLFile(path)
		if err != nil {
			return nil, err
		}
		sp := NewStaticProvider()
		sp.LoadRules(rules)
		providers = append(providers, sp)
		loadedCount = len(rules)
	}

	svc := NewService(NewChainProvider(providers...), opts...)
	if svc.logger != nil {
		svc.logger.Info("feature flags initialised",
			slog.String("file", path),
			slog.Int("rules", loadedCount),
			slog.String("env_prefix", EnvOverridePrefix),
		)
	}
	return svc, nil
}
