package service

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type agentSkillReaderStub struct {
	skills   []db.Skill
	skillErr error
	files    map[pgtype.UUID][]db.SkillFile
	filesErr map[pgtype.UUID]error
}

func (s agentSkillReaderStub) ListAgentSkills(context.Context, pgtype.UUID) ([]db.Skill, error) {
	return s.skills, s.skillErr
}

func (s agentSkillReaderStub) ListSkillFiles(_ context.Context, skillID pgtype.UUID) ([]db.SkillFile, error) {
	return s.files[skillID], s.filesErr[skillID]
}

func TestLoadAgentSkillsRejectsIncompleteSkillData(t *testing.T) {
	agentID := agentSkillsTestUUID(1)
	skillID := agentSkillsTestUUID(2)

	t.Run("skill list failure", func(t *testing.T) {
		want := errors.New("skill list unavailable")
		_, err := loadAgentSkills(context.Background(), agentSkillReaderStub{skillErr: want}, agentID)
		if !errors.Is(err, want) {
			t.Fatalf("loadAgentSkills error = %v, want wrapped %v", err, want)
		}
	})

	t.Run("skill file failure", func(t *testing.T) {
		want := errors.New("skill files unavailable")
		_, err := loadAgentSkills(context.Background(), agentSkillReaderStub{
			skills:   []db.Skill{{ID: skillID, Name: "release", Content: "instructions"}},
			filesErr: map[pgtype.UUID]error{skillID: want},
		}, agentID)
		if !errors.Is(err, want) {
			t.Fatalf("loadAgentSkills error = %v, want wrapped %v", err, want)
		}
	})
}

func TestLoadAgentSkillsBuildsCompleteProjection(t *testing.T) {
	agentID := agentSkillsTestUUID(3)
	skillID := agentSkillsTestUUID(4)
	skills, err := loadAgentSkills(context.Background(), agentSkillReaderStub{
		skills: []db.Skill{{
			ID: skillID, Name: "release", Description: "ship safely", Content: "instructions",
		}},
		files: map[pgtype.UUID][]db.SkillFile{
			skillID: {{Path: "checklist.md", Content: "verify"}},
		},
	}, agentID)
	if err != nil {
		t.Fatalf("loadAgentSkills: %v", err)
	}
	if len(skills) != 1 || len(skills[0].Files) != 1 {
		t.Fatalf("unexpected projection: %+v", skills)
	}
	if skills[0].Files[0].Path != "checklist.md" || skills[0].Files[0].Content != "verify" {
		t.Fatalf("unexpected file projection: %+v", skills[0].Files[0])
	}
}

func agentSkillsTestUUID(last byte) pgtype.UUID {
	var value [16]byte
	value[15] = last
	return pgtype.UUID{Bytes: value, Valid: true}
}
