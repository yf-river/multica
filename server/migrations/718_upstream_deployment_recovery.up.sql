ALTER TABLE upstream_deployment DROP CONSTRAINT upstream_deployment_action_check;
ALTER TABLE upstream_deployment ADD CONSTRAINT upstream_deployment_action_check CHECK(action IN ('prepare','deploy','recover'));
