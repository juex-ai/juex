CREATE TABLE runtime.observer_controls (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
 agent_id uuid NOT NULL REFERENCES runtime.agents(id),
 thread_id uuid NOT NULL REFERENCES runtime.threads(id),
 request_id text NOT NULL,
 start_request jsonb NOT NULL,
 scope jsonb NOT NULL,
 environment_id text NOT NULL,
 request jsonb NOT NULL,
 source_id uuid NOT NULL,
 mode text NOT NULL CHECK(mode IN ('once','continuous')),
 desired text NOT NULL DEFAULT 'running' CHECK(desired IN ('running','stopped')),
 state text NOT NULL DEFAULT 'pending',
 admitted boolean NOT NULL DEFAULT false,
 attempt bigint NOT NULL DEFAULT 1,
 next_check timestamptz NOT NULL DEFAULT clock_timestamp(),
 lease_epoch bigint NOT NULL DEFAULT 0,
 lease_until timestamptz NOT NULL DEFAULT '-infinity',
 lease_holder text NOT NULL DEFAULT '',
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 UNIQUE(agent_id,request_id)
);
ALTER TABLE runtime.observation_sources DROP CONSTRAINT observation_sources_id_fkey;
ALTER TABLE runtime.observation_sources ADD COLUMN origin_tool_id uuid REFERENCES runtime.tools(id),
 ADD COLUMN control_id uuid REFERENCES runtime.observer_controls(id),
 ADD COLUMN stop_requested boolean NOT NULL DEFAULT false;
UPDATE runtime.observation_sources SET origin_tool_id=id;
ALTER TABLE runtime.observation_sources ADD CONSTRAINT observation_source_origin CHECK((origin_tool_id IS NULL) <> (control_id IS NULL));
ALTER TABLE runtime.subscriptions ALTER COLUMN action_id DROP NOT NULL;
CREATE INDEX observer_controls_pending ON runtime.observer_controls(next_check,id) WHERE next_check<>'infinity';

CREATE TABLE runtime.observer_subscriptions (
 control_id uuid NOT NULL REFERENCES runtime.observer_controls(id),
 thread_id uuid NOT NULL REFERENCES runtime.threads(id),
 scope jsonb NOT NULL,
 kind text NOT NULL,
 method text NOT NULL DEFAULT '',
 enabled boolean NOT NULL,
 PRIMARY KEY(control_id,thread_id,kind,method)
);

ALTER TABLE runtime.observation_sources ADD COLUMN created_at timestamptz NOT NULL DEFAULT clock_timestamp();
UPDATE runtime.observation_sources o SET created_at=t.created_at FROM runtime.tools t WHERE t.id=o.origin_tool_id;
CREATE INDEX observation_sources_inspection ON runtime.observation_sources(agent_id,created_at DESC,id DESC);
