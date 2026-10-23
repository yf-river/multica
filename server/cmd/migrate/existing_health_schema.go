package main

import (
	"context"
	"crypto/sha256"
	"fmt"
	"reflect"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// 仅核对这两份历史 SQL，已有结构的正常迁移不能扩大为任意版本的台账补写。
var existingHealthSchemaSQLHashes = map[string]string{
	"501_health_data":         "6765c95f2ba6131ff305b186431e47d85beeb224ac9ae407d348a0a85a084d5b",
	"508_chat_health_reports": "210336b002c706daa5346be65e0edcfbfb8c3015eca5fb372e085abcbb883a7b",
}

func checkExistingHealthSQL(version string, sql []byte) error {
	if expected, ok := existingHealthSchemaSQLHashes[version]; ok && fmt.Sprintf("%x", sha256.Sum256(sql)) != expected {
		return fmt.Errorf("historical health migration %s SQL identity changed", version)
	}
	return nil
}

type healthSchemaColumn struct {
	name, kind  string
	notNull     bool
	defaultExpr string
}

var healthSchemaTables = map[string][]healthSchemaColumn{
	"health_state": {
		{"workspace_id", "uuid", true, ""},
		{"user_id", "uuid", true, ""},
		{"revision", "bigint", true, "0"},
		{"epoch", "bigint", true, "0"},
		{"data", "jsonb", true, `'{"profile": {}, "observation_ids": []}'::jsonb`},
		{"updated_at", "timestamp with time zone", true, "now()"},
	},
	"health_observation": {
		{"id", "uuid", true, ""},
		{"workspace_id", "uuid", true, ""},
		{"user_id", "uuid", true, ""},
		{"data", "jsonb", true, ""},
		{"created_at", "timestamp with time zone", true, "now()"},
	},
	"health_revision": {
		{"workspace_id", "uuid", true, ""},
		{"user_id", "uuid", true, ""},
		{"revision", "bigint", true, ""},
		{"data", "jsonb", true, ""},
		{"reason", "text", true, ""},
		{"created_at", "timestamp with time zone", true, "now()"},
	},
	"health_report": {
		{"id", "uuid", true, ""},
		{"workspace_id", "uuid", true, ""},
		{"user_id", "uuid", true, ""},
		{"filename", "text", true, ""},
		{"sha256", "text", true, ""},
		{"size_bytes", "bigint", true, ""},
		{"page_count", "integer", true, ""},
		{"issue_id", "uuid", false, ""},
		{"agent_id", "uuid", false, ""},
		{"attempt", "integer", true, "0"},
		{"epoch", "bigint", true, "0"},
		{"attempt_started_at", "timestamp with time zone", false, ""},
		{"created_at", "timestamp with time zone", true, "now()"},
	},
	"health_report_result": {
		{"id", "uuid", true, ""},
		{"report_id", "uuid", true, ""},
		{"workspace_id", "uuid", true, ""},
		{"user_id", "uuid", true, ""},
		{"attempt", "integer", true, ""},
		{"revision", "bigint", true, ""},
		{"data", "jsonb", true, ""},
		{"created_at", "timestamp with time zone", true, "now()"},
	},
}

func healthTableColumns(ctx context.Context, conn *pgxpool.Conn, table string) ([]healthSchemaColumn, bool, error) {
	var oid int64
	var plain bool
	err := conn.QueryRow(ctx, `SELECT c.oid::bigint,c.relkind='r' AND c.relpersistence='p' AND NOT c.relrowsecurity AND NOT c.relforcerowsecurity AND NOT c.relispartition FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname=current_schema() AND c.relname=$1`, table).Scan(&oid, &plain)
	if err == pgx.ErrNoRows {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if !plain {
		return nil, true, fmt.Errorf("%s is not an ordinary persistent table without row policies", table)
	}
	rows, err := conn.Query(ctx, `SELECT a.attname,format_type(a.atttypid,a.atttypmod),a.attnotnull,COALESCE(pg_get_expr(d.adbin,d.adrelid),''),a.attidentity<>'' OR a.attgenerated<>'' OR a.attcollation<>t.typcollation FROM pg_attribute a JOIN pg_type t ON t.oid=a.atttypid LEFT JOIN pg_attrdef d ON d.adrelid=a.attrelid AND d.adnum=a.attnum WHERE a.attrelid=$1 AND a.attnum>0 AND NOT a.attisdropped ORDER BY a.attnum`, oid)
	if err != nil {
		return nil, true, err
	}
	var columns []healthSchemaColumn
	for rows.Next() {
		var c healthSchemaColumn
		var generated bool
		if err = rows.Scan(&c.name, &c.kind, &c.notNull, &c.defaultExpr, &generated); err != nil {
			rows.Close()
			return nil, true, err
		}
		if generated {
			rows.Close()
			return nil, true, fmt.Errorf("%s.%s has unexpected identity/generated/collation semantics", table, c.name)
		}
		columns = append(columns, c)
	}
	err = rows.Err()
	rows.Close()
	return columns, true, err
}

func whenHealthSchemaMissing(ctx context.Context, conn *pgxpool.Conn) (bool, string, error) {
	present := 0
	for table, expected := range healthSchemaTables {
		columns, exists, err := healthTableColumns(ctx, conn, table)
		if err != nil {
			return false, "", err
		}
		if !exists {
			continue
		}
		present++
		if !reflect.DeepEqual(columns, expected) {
			return false, "", fmt.Errorf("existing %s columns do not exactly match migration 501", table)
		}
		var extras bool
		err = conn.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_constraint WHERE conrelid=to_regclass($1)) OR EXISTS(SELECT 1 FROM pg_trigger WHERE tgrelid=to_regclass($1) AND NOT tgisinternal) OR EXISTS(SELECT 1 FROM pg_inherits WHERE inhrelid=to_regclass($1) OR inhparent=to_regclass($1))`, table).Scan(&extras)
		if err != nil {
			return false, "", err
		}
		if extras {
			return false, "", fmt.Errorf("existing %s has unexpected constraints, triggers or inheritance", table)
		}
	}
	if present == 0 {
		return true, "", nil
	}
	if present != len(healthSchemaTables) {
		return false, "", fmt.Errorf("migration 501 health schema is only partially present")
	}
	return false, "all five existing health tables exactly match migration 501", nil
}

func whenChatHealthReportsMissing(ctx context.Context, conn *pgxpool.Conn) (bool, string, error) {
	columns, exists, err := healthTableColumns(ctx, conn, "chat_message")
	if err != nil {
		return false, "", err
	}
	if !exists {
		return false, "", fmt.Errorf("chat_message is missing")
	}
	expected := healthSchemaColumn{"health_report_ids", "uuid[]", true, "'{}'::uuid[]"}
	for _, column := range columns {
		if column.name == expected.name {
			if column != expected {
				return false, "", fmt.Errorf("existing chat_message.health_report_ids does not exactly match migration 508")
			}
			return false, "existing health_report_ids exactly matches migration 508", nil
		}
	}
	return true, "", nil
}
