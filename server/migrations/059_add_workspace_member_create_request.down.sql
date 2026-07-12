-- One-way current-version migration: workspace_member requests are durable
-- recovery witnesses and must not be destroyed by an application downgrade.
SELECT 1;
