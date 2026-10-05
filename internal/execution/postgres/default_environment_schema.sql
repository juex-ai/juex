CREATE TABLE execution.default_environments (
    agent_id uuid PRIMARY KEY,
    tenant_id uuid NOT NULL,
    user_id uuid NOT NULL,
    fleet_id uuid NOT NULL,
    environment_id uuid REFERENCES execution.environments(id),
    working_directory text NOT NULL DEFAULT '',
    version bigint NOT NULL CHECK (version > 0)
);
CREATE INDEX default_environments_fleet ON execution.default_environments(fleet_id);
ALTER TABLE execution.audit ALTER COLUMN environment_id DROP NOT NULL;
