//go:build !linux

package agent

import "errors"

func writeCodebuddyPoolRecord(string, string, string, string, string, int) error {
	return errors.New("resident CodeBuddy processes require Linux ownership")
}
func recoverCodebuddyPoolProcesses(string) error {
	return errors.New("resident CodeBuddy processes require Linux ownership")
}
