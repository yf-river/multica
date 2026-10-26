ALTER TABLE task_interaction ALTER COLUMN expires_at SET DEFAULT now() + interval '60 seconds';
