ALTER TABLE runtime.observation_sources DROP CONSTRAINT observation_sources_kind_check;
ALTER TABLE runtime.observation_sources ADD CONSTRAINT observation_sources_kind_check CHECK(kind IN ('exec_command','mcp_connect','observe_command'));
ALTER TABLE runtime.observation_sources
    ADD COLUMN options jsonb NOT NULL DEFAULT '{}',
    ADD COLUMN command_batch jsonb NOT NULL DEFAULT '{}',
    ADD COLUMN working_directory text NOT NULL DEFAULT '',
    ADD COLUMN authorization_version bigint NOT NULL DEFAULT 0;
ALTER TABLE runtime.subscriptions DROP CONSTRAINT subscriptions_kind_check;
ALTER TABLE runtime.subscriptions ADD CONSTRAINT subscriptions_kind_check CHECK(kind IN ('environment.presence','mcp.notification','operation.terminal','command.observation'));
DROP INDEX runtime.observation_sources_ack_pending;
CREATE INDEX observation_sources_ack_pending ON runtime.observation_sources(ack_next_check,id) WHERE kind IN ('mcp_connect','observe_command') AND confirmed_cursor<cursor;
