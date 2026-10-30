package upstream

import (
	"context"
	"regexp"
	"strings"
)

type Project struct {
	ID       string    `json:"id"`
	Name     string    `json:"name"`
	Enabled  bool      `json:"enabled"`
	Syncing  bool      `json:"syncing"`
	Snapshot *Snapshot `json:"snapshot,omitempty"`
}

var projectIDPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,63}$`)

func (s *Service) Projects(ctx context.Context) ([]Project, error) {
	rows, err := s.Pool.Query(ctx, `SELECT id,name,enabled FROM upstream_project ORDER BY created_at,id`)
	if err != nil {
		return nil, err
	}
	projects := []Project{}
	for rows.Next() {
		var p Project
		if err = rows.Scan(&p.ID, &p.Name, &p.Enabled); err != nil {
			rows.Close()
			return nil, err
		}
		projects = append(projects, p)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	for i := range projects {
		snap, e := s.ForProject(projects[i].ID).Snapshot(ctx)
		if e != nil {
			return nil, e
		}
		if err := s.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM agent_task_queue WHERE agent_id=NULLIF($1,'')::uuid AND status IN ('queued','dispatched','running','waiting_local_directory','deferred'))`, snap.Config.AgentID).Scan(&projects[i].Syncing); err != nil {
			return nil, err
		}
		projects[i].Snapshot = snap
	}
	return projects, nil
}

func (s *Service) SaveProject(ctx context.Context, p Project) error {
	if !projectIDPattern.MatchString(p.ID) || strings.TrimSpace(p.Name) == "" || len(p.Name) > 200 {
		return ValidationError{Field: "project", Message: "项目标识或名称无效"}
	}
	_, err := s.Pool.Exec(ctx, `INSERT INTO upstream_project(id,name,enabled) VALUES($1,$2,$3)
 ON CONFLICT(id) DO UPDATE SET name=EXCLUDED.name,enabled=EXCLUDED.enabled,updated_at=now()`, p.ID, strings.TrimSpace(p.Name), p.Enabled)
	return err
}
