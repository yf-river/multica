package agent

// 线程级覆盖高于用户配置与启动参数，恢复会话也必须重新执行平台的子代理策略。
func codexSubagentConfig(version string) map[string]any {
	config := map[string]any{"features.multi_agent": false}
	if parsed, err := parseSemver(version); err == nil && !parsed.lessThan(semver{Minor: 164}) {
		config["features.multi_agent_v2"] = false
		config["agents.enabled"] = false
	}
	return config
}
