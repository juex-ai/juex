CREATE TABLE execution.purges (
    id uuid PRIMARY KEY,
    target jsonb NOT NULL,
    fleet_id uuid NOT NULL,
    agent_ids uuid[] NOT NULL,
    whole_fleet boolean NOT NULL,
    erased boolean NOT NULL DEFAULT false
);
ALTER TABLE execution.operations ADD COLUMN purged boolean NOT NULL DEFAULT false;
ALTER TABLE execution.artifacts ADD COLUMN purge_blocked boolean NOT NULL DEFAULT false;
CREATE TABLE execution.purge_operations (
    purge_id uuid NOT NULL REFERENCES execution.purges(id),
    environment_id uuid NOT NULL,
    operation_id text NOT NULL,
    PRIMARY KEY(purge_id,environment_id,operation_id)
);
ALTER TABLE execution.hosted ADD COLUMN purging boolean NOT NULL DEFAULT false;
ALTER TABLE execution.hosted ADD COLUMN purge_data boolean NOT NULL DEFAULT false;
