package daemon

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestDeploymentDrainWaitsForExistingTasks(t *testing.T) {
	d := &Daemon{}
	d.activeTasks.Store(1)
	r := httptest.NewRecorder()
	d.drainHandler()(r, httptest.NewRequest(http.MethodPost, "/drain", nil))
	var state map[string]any
	json.Unmarshal(r.Body.Bytes(), &state)
	if state["ready_to_stop"] != false || !d.deploymentDrain {
		t.Fatal(state)
	}
	if d.trySetClaimBarrier() {
		t.Fatal("update acquired while draining")
	}
	d.activeTasks.Store(0)
	r = httptest.NewRecorder()
	d.drainHandler()(r, httptest.NewRequest(http.MethodGet, "/drain", nil))
	json.Unmarshal(r.Body.Bytes(), &state)
	if state["ready_to_stop"] != true {
		t.Fatal(state)
	}
	d.drainHandler()(httptest.NewRecorder(), httptest.NewRequest(http.MethodDelete, "/drain", nil))
	if d.deploymentDrain {
		t.Fatal("did not resume")
	}
}
func TestEnvironmentWritePathRejectsSiblingAndSymlink(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "int")
	other := filepath.Join(parent, "prod")
	os.Mkdir(root, 0700)
	os.Mkdir(other, 0700)
	t.Setenv("MULTICA_ENV_WRITABLE_ROOT", root)
	if err := validateEnvironmentWritePath(root); err != nil {
		t.Fatal(err)
	}
	if validateEnvironmentWritePath(other) == nil {
		t.Fatal("sibling accepted")
	}
	link := filepath.Join(root, "outside")
	if err := os.Symlink(other, link); err != nil {
		t.Skip(err)
	}
	if validateEnvironmentWritePath(link) == nil {
		t.Fatal("symlink escape accepted")
	}
}
