package cli

import (
	"os"
	"strings"

	"github.com/spf13/cobra"
)

// FlagOrEnv returns the flag value if set, otherwise the environment value.
func FlagOrEnv(cmd *cobra.Command, flagName, envKey string) string {
	if cmd.Flags().Changed(flagName) {
		val, _ := cmd.Flags().GetString(flagName)
		return val
	}
	if v := strings.TrimSpace(os.Getenv(envKey)); v != "" {
		return v
	}
	return ""
}
