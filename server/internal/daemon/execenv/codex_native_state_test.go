//go:build linux

package execenv

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func codexNativeStateHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte("model = 'configured-model'\n"), 0600); err != nil {
		t.Fatal(err)
	}
	return home
}

func TestCodexNativeStateSnapshotPreservesWALAndSurvivesTaskHomeRemoval(t *testing.T) {
	source, current, store := codexNativeStateHome(t), codexNativeStateHome(t), filepath.Join(t.TempDir(), "conversation")
	database, err := sql.Open("sqlite", filepath.Join(source, "goals_1.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	database.SetMaxOpenConns(1)
	if _, err := database.Exec(`PRAGMA journal_mode=WAL; PRAGMA wal_autocheckpoint=0;
CREATE TABLE native_goal_fixture (objective TEXT, tokens_used INTEGER, time_used INTEGER);
INSERT INTO native_goal_fixture (rowid,objective,tokens_used,time_used) VALUES (835719,'original native objective',739521,864203)`); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(filepath.Join(source, "goals_1.sqlite-wal")); err != nil || info.Size() == 0 {
		t.Fatal("fixture did not retain committed WAL data")
	}
	const session = "01990000-1111-7222-8333-444444444444"
	const content = "{\"type\":\"session_meta\",\"payload\":{\"id\":\"" + session + "\"}}\noriginal-native-rollout\n"
	rollout := filepath.Join(source, "sessions", "2026", "10", "26", "rollout-2026-10-26-"+session+".jsonl")
	if err := os.MkdirAll(filepath.Dir(rollout), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(rollout, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	retired := false
	state, err := PrepareCodexNativeState(t.Context(), current, source, store, "runtime-one", session, func(_ context.Context, home string) error {
		if home != source {
			t.Fatal("retired a home outside the exact source")
		}
		retired = true
		return nil
	})
	if err != nil || !retired || state.ResumePath == "" {
		t.Fatalf("native snapshot: %+v retired=%t error=%v", state, retired, err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(source); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(current); err != nil {
		t.Fatal(err)
	}
	next := codexNativeStateHome(t)
	resumed, err := PrepareCodexNativeState(t.Context(), next, "", store, "runtime-one", session, func(context.Context, string) error {
		t.Fatal("stable state unnecessarily retired the warm process")
		return nil
	})
	if err != nil || resumed.SQLiteHome != state.SQLiteHome || resumed.ResumePath != state.ResumePath {
		t.Fatalf("next task native state: %+v %v", resumed, err)
	}
	copy, err := sql.Open("sqlite", filepath.Join(resumed.SQLiteHome, "goals_1.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer copy.Close()
	var objective string
	var rowID, tokens, elapsed int64
	if err := copy.QueryRow("SELECT rowid,objective,tokens_used,time_used FROM native_goal_fixture").Scan(&rowID, &objective, &tokens, &elapsed); err != nil || rowID != 835719 || objective != "original native objective" || tokens != 739521 || elapsed != 864203 {
		t.Fatalf("native accounting changed: objective=%q tokens=%d elapsed=%d error=%v", objective, tokens, elapsed, err)
	}
	if data, err := os.ReadFile(resumed.ResumePath); err != nil || string(data) != content {
		t.Fatal("resume rollout depended on the removed task home")
	}
	if link, err := filepath.EvalSymlinks(filepath.Join(next, "sessions")); err != nil || link != store {
		t.Fatal("new native rollouts do not use persistent conversation storage")
	}
	if CodexNativeStateDir(store, "runtime-two") == state.SQLiteHome {
		t.Fatal("different runtimes shared native SQLite state")
	}
}

func TestCodexNativeStateRejectsMissingCorruptAndLinkedSources(t *testing.T) {
	for _, kind := range []string{"missing", "corrupt", "symlink"} {
		t.Run(kind, func(t *testing.T) {
			home, source, store := codexNativeStateHome(t), codexNativeStateHome(t), filepath.Join(t.TempDir(), "conversation")
			if kind == "corrupt" {
				if err := os.WriteFile(filepath.Join(source, "goals_1.sqlite"), []byte("not-a-database"), 0600); err != nil {
					t.Fatal(err)
				}
			} else if kind == "symlink" {
				if err := os.Symlink(filepath.Join(home, "config.toml"), filepath.Join(source, "goals_1.sqlite")); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := PrepareCodexNativeState(t.Context(), home, source, store, "runtime", "existing-session", nil); err == nil {
				t.Fatal("unavailable native state silently created an empty resumed Goal")
			}
			if _, err := os.Stat(CodexNativeStateDir(store, "runtime")); !os.IsNotExist(err) {
				t.Fatal("incomplete snapshot became authoritative")
			}
		})
	}
}

func TestCodexNativeStatePreservesUserSQLiteConfiguration(t *testing.T) {
	home, store := codexNativeStateHome(t), filepath.Join(t.TempDir(), "untouched")
	if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte("sqlite_home = '/user/chosen/native-state'\n"), 0600); err != nil {
		t.Fatal(err)
	}
	state, err := PrepareCodexNativeState(t.Context(), home, "", store, "runtime", "prior", nil)
	if err != nil || state != (CodexNativeState{}) {
		t.Fatal("managed snapshot replaced user-selected native storage")
	}
	if _, err := os.Stat(store); !os.IsNotExist(err) {
		t.Fatal("user-selected native storage triggered a managed snapshot")
	}
}

func TestCodexNativeSnapshotRelocatesIndexWithoutChangingSourceOrPagedHistory(t *testing.T) {
	source, current, store := codexNativeStateHome(t), codexNativeStateHome(t), t.TempDir()
	const id = "01990000-1111-7222-8333-444444444444"
	rollout := filepath.Join(source, "sessions", "rollout-2026-10-26-"+id+".jsonl")
	if err := os.MkdirAll(filepath.Dir(rollout), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(rollout, []byte(`{"type":"session_meta","payload":{"id":"`+id+`"}}`+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	index, err := sql.Open("sqlite", filepath.Join(source, "state_5.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer index.Close()
	if _, err := index.Exec("CREATE TABLE threads (id TEXT PRIMARY KEY, rollout_path TEXT NOT NULL, archived INTEGER NOT NULL, tokens_used INTEGER NOT NULL, history_mode TEXT NOT NULL)"); err != nil {
		t.Fatal(err)
	}
	if _, err := index.Exec("INSERT INTO threads VALUES (?,?,0,739521,'paginated')", id, rollout); err != nil {
		t.Fatal(err)
	}
	history, err := sql.Open("sqlite", filepath.Join(source, "thread_history_1.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer history.Close()
	if _, err := history.Exec("CREATE TABLE thread_items (thread_id TEXT, item_json TEXT); INSERT INTO thread_items VALUES (?,'original native page')", id); err != nil {
		t.Fatal(err)
	}
	state, err := PrepareCodexNativeState(t.Context(), current, source, store, "runtime", id, nil)
	if err != nil {
		t.Fatal(err)
	}
	cloned, err := sql.Open("sqlite", filepath.Join(state.SQLiteHome, "state_5.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer cloned.Close()
	var path, mode string
	var tokens int
	if err := cloned.QueryRow("SELECT rollout_path,tokens_used,history_mode FROM threads WHERE id=?", id).Scan(&path, &tokens, &mode); err != nil || path != state.ResumePath || tokens != 739521 || mode != "paginated" {
		t.Fatalf("native index relocation changed other fields: %q %d %q %v", path, tokens, mode, err)
	}
	if err := index.QueryRow("SELECT rollout_path FROM threads WHERE id=?", id).Scan(&path); err != nil || path != rollout {
		t.Fatal("snapshot changed its source index")
	}
	clonedHistory, err := sql.Open("sqlite", filepath.Join(state.SQLiteHome, "thread_history_1.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer clonedHistory.Close()
	var item string
	if err := clonedHistory.QueryRow("SELECT item_json FROM thread_items WHERE thread_id=?", id).Scan(&item); err != nil || item != "original native page" {
		t.Fatal("native paged history changed during relocation")
	}
}

func TestCodexNativeRolloutMigrationRetainsOriginalAndRefusesConflicts(t *testing.T) {
	home, store := codexNativeStateHome(t), t.TempDir()
	path := filepath.Join(home, "sessions", "rollout-original.jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(store, filepath.Base(path))
	if err := os.WriteFile(destination, []byte("conflict"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := preserveCodexNativeRollouts(t.Context(), home, store); err == nil {
		t.Fatal("replaced independent canonical history")
	}
	if data, _ := os.ReadFile(path); string(data) != "original" {
		t.Fatal("migration altered its source")
	}
	if err := os.WriteFile(destination, []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := preserveCodexNativeRollouts(t.Context(), home, store); err != nil {
		t.Fatal(err)
	}
	backups, _ := filepath.Glob(filepath.Join(home, ".sessions-backup-*"))
	if len(backups) != 1 || !strings.HasPrefix(backups[0], home+string(os.PathSeparator)) {
		t.Fatal("migration did not retain the original directory")
	}
	if data, _ := os.ReadFile(filepath.Join(backups[0], filepath.Base(path))); string(data) != "original" {
		t.Fatal("original rollout backup is unavailable")
	}
}
