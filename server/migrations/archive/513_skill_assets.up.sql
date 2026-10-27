ALTER TABLE skill_file
 ADD COLUMN kind text NOT NULL DEFAULT 'text' CHECK (kind IN ('text', 'binary')),
 ADD COLUMN storage_key text NOT NULL DEFAULT '',
 ADD COLUMN content_type text NOT NULL DEFAULT 'text/plain; charset=utf-8',
 ADD COLUMN size_bytes bigint NOT NULL DEFAULT 0,
 ADD COLUMN sha256 text NOT NULL DEFAULT '',
 ADD COLUMN mode integer NOT NULL DEFAULT 420 CHECK (mode IN (420, 493));
CREATE TABLE skill_asset (
 storage_key text NOT NULL,
 workspace_id uuid NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now()
);
