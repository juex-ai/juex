ALTER TABLE management.agents ADD COLUMN agent_management boolean NOT NULL DEFAULT false;
ALTER TABLE management.audit ADD COLUMN source_agent_id uuid;
ALTER TABLE management.audit ADD COLUMN source_thread_id uuid;
ALTER TABLE management.audit ADD COLUMN control_action_id uuid;
CREATE TABLE management.agent_control_receipts (
 source_agent_id uuid NOT NULL REFERENCES management.agents(id) ON DELETE CASCADE,
 action_id uuid NOT NULL,
 source jsonb NOT NULL,
 request jsonb NOT NULL,
 result jsonb NOT NULL,
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY(source_agent_id,action_id)
);
