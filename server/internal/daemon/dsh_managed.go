package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/daemon/processtree"
	"github.com/multica-ai/multica/server/pkg/agent"
	"github.com/multica-ai/multica/server/pkg/dshprofile"
)

type dshSupplyStatus struct {
	Status     string                `json:"status"`
	Identity   string                `json:"identity"`
	Generation dshprofile.Generation `json:"generation"`
	Error      string                `json:"error,omitempty"`
}

type dshSupplyHealth struct {
	Status                string `json:"status"`
	BridgeVersion         string `json:"bridge_version,omitempty"`
	DSHVersion            string `json:"dsh_version,omitempty"`
	SHA256                string `json:"sha256,omitempty"`
	PreviousBridgeVersion string `json:"previous_bridge_version,omitempty"`
}

func managedDshHealth() *dshSupplyHealth {
	root, _, enabled := dshSupplyConfig()
	if !enabled {
		return nil
	}
	status := readDshSupplyStatus(root)
	view := &dshSupplyHealth{Status: status.Status}
	if view.Status == "" {
		view.Status = "pending"
	}
	state, err := dshprofile.Read(root)
	if err != nil {
		view.Status = "degraded"
		return view
	}
	if state != nil {
		view.BridgeVersion = state.Active.BridgeVersion
		view.DSHVersion = state.Active.DSHVersion
		view.SHA256 = state.Active.SHA256
		if state.Previous != nil {
			view.PreviousBridgeVersion = state.Previous.BridgeVersion
		}
	}
	return view
}

func dshSupplyConfig() (string, string, bool) {
	if os.Getenv("MULTICA_DSH_MANAGED_PROFILE") == "0" {
		return "", "", false
	}
	root, artifacts := strings.TrimSpace(os.Getenv(dshprofile.StateEnv)), strings.TrimSpace(os.Getenv(dshprofile.ArtifactEnv))
	return root, artifacts, root != "" && artifacts != ""
}

func readDshSupplyStatus(root string) dshSupplyStatus {
	var status dshSupplyStatus
	if data, err := os.ReadFile(filepath.Join(root, "status.json")); err == nil {
		_ = json.Unmarshal(data, &status)
	}
	return status
}

// prepareManagedDsh 在生命周期上下文准备候选，发现轮不等待网络或真实模型 smoke。
func (d *Daemon) prepareManagedDsh(path, version string) {
	path = canonicalExecutablePath(path)
	root, artifacts, enabled := dshSupplyConfig()
	if !enabled {
		return
	}
	manifest, err := dshprofile.ReadManifest(artifacts)
	if err != nil {
		d.logger.Warn("DSH managed artifact unavailable", "error", err)
		return
	}
	state, err := dshprofile.Read(root)
	if err != nil {
		d.logger.Warn("DSH managed state unreadable", "error", err)
		return
	}
	if state != nil && state.Active.SHA256 == manifest.SHA256 && state.Active.DSHVersion == version && state.Active.DSHPath == path {
		return
	}
	identity := manifest.SHA256 + ":" + version + ":" + path
	previous := readDshSupplyStatus(root)
	if previous.Identity == identity && previous.Status == "degraded" {
		return
	}
	if !d.dshInstallInFlight.CompareAndSwap(false, true) {
		return
	}
	go func() {
		defer d.dshInstallInFlight.Store(false)
		ctx, cancel := context.WithTimeout(d.daemonLifecycleCtx(), 5*time.Minute)
		defer cancel()
		err := d.supplyManagedDsh(ctx, root, artifacts, path, version, manifest)
		if err != nil && !errors.Is(err, dshprofile.ErrBusy) {
			d.logger.Warn("DSH managed supply degraded; active generation retained", "error", err)
		}
		d.registerAfterDshProfileInstall(ctx)
	}()
}

func (d *Daemon) supplyManagedDsh(ctx context.Context, root, artifacts, path, version string, m dshprofile.Manifest) error {
	preparation, err := dshprofile.Lock(filepath.Join(root, "preparation"), true)
	if err != nil {
		return err
	}
	defer preparation.Close()
	identity := m.SHA256 + ":" + version + ":" + path
	status := readDshSupplyStatus(root)
	if status.Identity == identity && status.Status == "degraded" {
		return errors.New(status.Error)
	}
	fail := func(cause error) error {
		status.Status = "degraded"
		status.Error = dshProvisionOutput([]byte(cause.Error()))
		if e := dshprofile.WriteJSON(root, "status.json", status); e != nil {
			return fmt.Errorf("persist DSH failure: %w", e)
		}
		return errors.New(status.Error)
	}
	if status.Identity != identity {
		status = dshSupplyStatus{Status: "preparing", Identity: identity, Generation: dshprofile.Generation{
			Profile: dshprofile.ProfileName(root, identity+uuid.NewString()), DSHPath: path, DSHVersion: version, BridgeVersion: m.BridgeVersion, SHA256: m.SHA256,
		}}
	}
	if !dshprofile.Supports(version, m.DSHRange) {
		return fail(fmt.Errorf("DSH %s is outside bridge supported range %s", version, m.DSHRange))
	}
	artifact, err := dshprofile.VerifyArtifact(artifacts, m)
	if err != nil {
		return fail(err)
	}
	if status.Status != "validated" {
		if err = dshprofile.WriteJSON(root, "status.json", status); err != nil {
			return err
		}
		cmd := exec.Command(path, "plugin", "--profile", status.Generation.Profile, "add", artifact, "--offline")
		cmd.Env = dshPluginEnv()
		output, e := processtree.CombinedOutput(ctx, cmd, 5*time.Second)
		if e != nil {
			return fail(fmt.Errorf("DSH staging install: %w: %s", e, dshProvisionOutput(output)))
		}
		if err = probeManagedDsh(ctx, status.Generation); err != nil {
			return fail(err)
		}
		if err = smokeManagedDsh(ctx, root, status.Generation, d.logger); err != nil {
			return fail(err)
		}
		status.Status = "validated"
		if err = dshprofile.WriteJSON(root, "status.json", status); err != nil {
			return err
		}
	}
	// 本 daemon 的准备/认领屏障与跨进程执行锁同时成立后才切换。
	if d.tryBeginServerUpdate(ctx) != serverUpdateAcquired {
		return dshprofile.ErrBusy
	}
	defer d.updating.Store(false)
	defer d.releaseClaimBarrier()
	admission, err := dshprofile.Lock(root, true)
	if err != nil {
		return err
	}
	defer admission.Close()
	old, err := dshprofile.Read(root)
	if err != nil {
		return fail(err)
	}
	next := dshprofile.State{Active: status.Generation}
	next.Active.ActivatedAt = time.Now().UTC()
	if old != nil {
		next.Previous = &old.Active
	}
	if err = dshprofile.Save(root, next); err != nil {
		return fail(err)
	}
	// 激活后的 probe 失败时原子恢复原记录；保留候选供调查。
	if err = probeManagedDsh(ctx, next.Active); err != nil {
		if old != nil {
			if e := dshprofile.Save(root, *old); e != nil {
				return fail(fmt.Errorf("DSH activation and rollback failed: %w", e))
			}
		} else {
			if e := os.Remove(filepath.Join(root, "active.json")); e != nil {
				return fail(fmt.Errorf("DSH initial activation cleanup failed: %w", e))
			}
		}
		return fail(err)
	}
	status.Status = "active"
	status.Generation = next.Active
	status.Error = ""
	return dshprofile.WriteJSON(root, "status.json", status)
}

func probeManagedDsh(ctx context.Context, g dshprofile.Generation) error {
	ctx, cancel := context.WithTimeout(ctx, dshProbeTimeout)
	defer cancel()
	cmd := exec.Command(g.DSHPath, "--profile", g.Profile, "--probe")
	output, err := processtree.Output(ctx, cmd, time.Second)
	if err != nil {
		return fmt.Errorf("DSH staging probe failed: %w", err)
	}
	frame, ok := parseDshProbeFrame(string(output))
	if !ok || frame.Version != 1 || frame.Runtime != "dsh" || frame.ProtocolVersion != agent.DshProtocolVersion || frame.PluginVersion != g.BridgeVersion {
		return errors.New("DSH staging protocol or bridge identity mismatch")
	}
	return nil
}

func smokeManagedDsh(ctx context.Context, root string, g dshprofile.Generation, logger *slog.Logger) error {
	work, err := os.MkdirTemp(root, "canary-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(work)
	b, err := agent.New("dsh", agent.Config{ExecutablePath: g.DSHPath, Logger: logger, Env: map[string]string{
		dshprofile.StateEnv: "", "MULTICA_DSH_PROFILE": g.Profile, "MULTICA_DSH_SESSION_ROOT": filepath.Join(work, "sessions"), "DSH_TELEMETRY_DISABLED": "1",
	}})
	if err != nil {
		return err
	}
	run := func(prompt, command, sessionID string) (agent.Result, bool, error) {
		session, e := b.Execute(ctx, prompt, agent.ExecOptions{Cwd: work, Model: os.Getenv("MULTICA_DSH_CANARY_MODEL"), Command: command, ResumeSessionID: sessionID, Timeout: 90 * time.Second})
		if e != nil {
			return agent.Result{}, false, e
		}
		contextSeen := false
		for msg := range session.Messages {
			if msg.Type == agent.MessageRuntime {
				var v struct {
					Kind string `json:"kind"`
				}
				if json.Unmarshal([]byte(msg.Content), &v) == nil && v.Kind == "context" {
					contextSeen = true
				}
			}
		}
		result := <-session.Result
		if result.Status != "completed" || result.SessionID == "" {
			return result, contextSeen, fmt.Errorf("DSH canary failed: %s", result.Error)
		}
		return result, contextSeen, nil
	}
	first, seen, err := run("Remember marker MULTICA_DSH_CANARY_OK. Reply with exactly that marker.", "", "")
	if err != nil {
		return err
	}
	if !seen || strings.TrimSpace(first.Output) != "MULTICA_DSH_CANARY_OK" {
		return errors.New("DSH canary output or context frame missing")
	}
	plan, _, err := run("", "/plan", first.SessionID)
	if err != nil {
		return err
	}
	if plan.SessionID != first.SessionID || len(plan.Usage) != 0 {
		return errors.New("DSH plan command changed session or called a model")
	}
	leave, _, err := run("", "/plan off", first.SessionID)
	if err != nil {
		return err
	}
	if leave.SessionID != first.SessionID {
		return errors.New("DSH plan exit changed session")
	}
	last, seen, err := run("Reply with exactly the remembered marker.", "", first.SessionID)
	if err != nil {
		return err
	}
	if last.SessionID != first.SessionID || !seen || strings.TrimSpace(last.Output) != "MULTICA_DSH_CANARY_OK" {
		return errors.New("DSH canary session resume failed")
	}
	return nil
}
