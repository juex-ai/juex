CREATE TABLE calendar.purges (
    id uuid PRIMARY KEY,
    target jsonb NOT NULL,
    fleet_id uuid NOT NULL,
    agent_ids uuid[] NOT NULL,
    whole_fleet boolean NOT NULL,
    erased boolean NOT NULL DEFAULT false
);
