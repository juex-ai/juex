CREATE TABLE runtime.instruction_preparations (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
 turn_id uuid NOT NULL REFERENCES runtime.turns(id),
 thread_id uuid NOT NULL REFERENCES runtime.threads(id),
 scope jsonb NOT NULL,
 config jsonb NOT NULL,
 environment_id text NOT NULL DEFAULT '',
 request jsonb,
 state text NOT NULL DEFAULT 'pending' CHECK(state IN ('pending','waiting','ready','failed','cancelled','unknown')),
 snapshot jsonb,
 error text NOT NULL DEFAULT '',
 output_cursor bigint NOT NULL DEFAULT 0 CHECK(output_cursor>=0),
 output_acknowledged boolean NOT NULL DEFAULT false,
 consumed_attempt uuid REFERENCES runtime.attempts(id),
 cancel_requested boolean NOT NULL DEFAULT false,
 next_check timestamptz NOT NULL DEFAULT clock_timestamp(),
 lease_epoch bigint NOT NULL DEFAULT 0,
 wake_version bigint NOT NULL DEFAULT 0,
 lease_until timestamptz NOT NULL DEFAULT '-infinity',
 lease_holder text NOT NULL DEFAULT '',
 created_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
CREATE UNIQUE INDEX instruction_preparation_current ON runtime.instruction_preparations(turn_id) WHERE consumed_attempt IS NULL;
CREATE INDEX instruction_preparation_pending ON runtime.instruction_preparations(next_check) WHERE state IN ('pending','waiting') OR NOT output_acknowledged;
