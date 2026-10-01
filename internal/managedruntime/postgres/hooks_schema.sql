CREATE TABLE runtime.hooks (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
 turn_id uuid NOT NULL REFERENCES runtime.turns(id),
 thread_id uuid NOT NULL REFERENCES runtime.threads(id),
 event text NOT NULL,
 anchor text NOT NULL,
 hook_id text NOT NULL,
 ordinal integer NOT NULL,
 scope jsonb NOT NULL,
 declaration jsonb NOT NULL,
 input jsonb NOT NULL,
 environment_id text NOT NULL DEFAULT '',
 request jsonb,
 state text NOT NULL DEFAULT 'pending' CHECK(state IN ('pending','waiting','completed','failed','cancelled','unknown')),
 result jsonb,
 applied boolean NOT NULL DEFAULT false,
 cancel_requested boolean NOT NULL DEFAULT false,
 next_check timestamptz NOT NULL DEFAULT clock_timestamp(),
 lease_epoch bigint NOT NULL DEFAULT 0,
 wake_version bigint NOT NULL DEFAULT 0,
 lease_until timestamptz NOT NULL DEFAULT '-infinity',
 lease_holder text NOT NULL DEFAULT '',
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 UNIQUE(thread_id,event,anchor,hook_id)
);
CREATE INDEX hooks_pending ON runtime.hooks(next_check,created_at) WHERE state IN ('pending','waiting');
CREATE INDEX hooks_turn ON runtime.hooks(turn_id);
ALTER TABLE runtime.tools ADD COLUMN deferred_result jsonb;
ALTER TABLE runtime.tools ADD COLUMN hook_context text NOT NULL DEFAULT '';
ALTER TABLE runtime.turns ADD COLUMN deferred_finish jsonb;
ALTER TABLE runtime.turns ADD COLUMN hook_continuations integer NOT NULL DEFAULT 0;
ALTER TABLE runtime.threads ADD COLUMN hook_start_turn uuid;
ALTER TABLE runtime.tools DROP CONSTRAINT tools_waiting_reason_check;
ALTER TABLE runtime.tools ADD CONSTRAINT tools_waiting_reason_check CHECK(waiting_reason IN ('','environment','execution','hook'));
