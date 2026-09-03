package daemon

import (
	"encoding/json"
	"fmt"
	"os"
	"time"
)

const governedLifeTaskTimeout = 10 * time.Minute

func governedLifeMCPConfig() (json.RawMessage, error) {
	executable, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("resolve multica executable for Life MCP: %w", err)
	}
	config, err := json.Marshal(map[string]any{
		"mcpServers": map[string]any{
			"life": map[string]any{"command": executable, "args": []string{"life", "mcp"}},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("build Life MCP config: %w", err)
	}
	return config, nil
}

func taskExecutionTimeout(configured time.Duration, governedLifeTask bool) time.Duration {
	if governedLifeTask && (configured <= 0 || configured > governedLifeTaskTimeout) {
		return governedLifeTaskTimeout
	}
	return configured
}
