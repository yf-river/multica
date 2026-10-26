package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/spf13/cobra"
)

func init() {
	daemonCmd.AddCommand(&cobra.Command{Use: "dsh-retry", Short: "Retry the failed DSH bridge candidate after fixing credentials or network", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		if err := requireHumanLocalCommand("daemon dsh-retry"); err != nil {
			return err
		}
		profile := resolveProfile(cmd)
		if err := requireKnownProfile(profile); err != nil {
			return err
		}
		port := healthPortForProfile(profile)
		ctx, cancel := context.WithTimeout(cmd.Context(), 5*time.Second)
		defer cancel()
		health := checkDaemonHealthOnPort(ctx, port)
		if health == nil {
			return fmt.Errorf("daemon is not running")
		}
		if err := daemonIdentityMismatch(health, profile, port); err != nil {
			return err
		}
		request, err := http.NewRequestWithContext(ctx, http.MethodPost, fmt.Sprintf("http://127.0.0.1:%d/dsh-profile/retry", port), nil)
		if err != nil {
			return err
		}
		request.Header.Set("Content-Type", "application/json")
		client := &http.Client{Transport: &http.Transport{Proxy: nil}, Timeout: 5 * time.Second}
		response, err := client.Do(request)
		if err != nil {
			return err
		}
		defer response.Body.Close()
		body, err := io.ReadAll(io.LimitReader(response.Body, 4096))
		if err != nil {
			return err
		}
		if response.StatusCode != http.StatusOK {
			return fmt.Errorf("DSH retry refused: %s", body)
		}
		_, err = fmt.Fprint(cmd.OutOrStdout(), string(body))
		return err
	}})
}
