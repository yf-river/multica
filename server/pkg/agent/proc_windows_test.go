//go:build windows

package agent

import (
	"os/exec"
	"syscall"
	"testing"
)

func TestHideAgentWindowSetsCreateNewConsole(t *testing.T) {
	cmd := exec.Command("cmd.exe", "/c", "echo", "hi")
	hideAgentWindow(cmd)

	if cmd.SysProcAttr == nil {
		t.Fatal("SysProcAttr should be initialized")
	}
	if !cmd.SysProcAttr.HideWindow {
		t.Error("HideWindow should be true")
	}
	if cmd.SysProcAttr.CreationFlags&createNewConsole == 0 {
		t.Errorf("CreationFlags should include CREATE_NEW_CONSOLE (0x%x), got 0x%x",
			createNewConsole, cmd.SysProcAttr.CreationFlags)
	}
	const createNoWindow = 0x08000000
	if cmd.SysProcAttr.CreationFlags&createNoWindow != 0 {
		t.Errorf("CreationFlags must not include CREATE_NO_WINDOW (0x%x), got 0x%x",
			createNoWindow, cmd.SysProcAttr.CreationFlags)
	}
}

func TestHideAgentWindowPreservesExistingSysProcAttr(t *testing.T) {
	const createUnicodeEnvironment = 0x00000400
	cmd := exec.Command("cmd.exe", "/c", "echo", "hi")
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags:    createUnicodeEnvironment,
		NoInheritHandles: true,
	}
	hideAgentWindow(cmd)

	if !cmd.SysProcAttr.NoInheritHandles {
		t.Error("NoInheritHandles set by caller should be preserved")
	}
	if cmd.SysProcAttr.CreationFlags&createUnicodeEnvironment == 0 {
		t.Error("existing CreationFlags bits (CREATE_UNICODE_ENVIRONMENT) should be preserved")
	}
	if cmd.SysProcAttr.CreationFlags&createNewConsole == 0 {
		t.Error("CREATE_NEW_CONSOLE should be OR'd into existing flags")
	}
}
