-- One-way current-version migration: completed experiment requests remain
-- durable recovery witnesses until the standard retention job removes them.
SELECT 1;
