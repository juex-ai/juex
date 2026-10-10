ALTER TABLE runtime.threads ADD COLUMN input_scope uuid NOT NULL DEFAULT gen_random_uuid();
CREATE TABLE runtime.input_tracking (
 thread_id uuid NOT NULL REFERENCES runtime.threads(id) ON DELETE CASCADE,
 input_id uuid NOT NULL,
 scope_id uuid NOT NULL,
 accepted_order bigint NOT NULL CHECK(accepted_order>0),
 message_id uuid,
 delivery text NOT NULL DEFAULT 'registered' CHECK(delivery IN ('registered','delivered','blocked')),
 checked_at timestamptz,
 check_action_id uuid,
 check_message_id uuid,
 tool_use_id text,
 PRIMARY KEY(thread_id,input_id),
 UNIQUE(thread_id,message_id),
 CHECK(delivery<>'delivered' OR message_id IS NOT NULL),
 CHECK((checked_at IS NULL AND check_action_id IS NULL AND check_message_id IS NULL AND tool_use_id IS NULL)
    OR (checked_at IS NOT NULL AND check_action_id IS NOT NULL AND check_message_id IS NOT NULL AND tool_use_id IS NOT NULL AND delivery='delivered'))
);
CREATE INDEX input_tracking_unchecked ON runtime.input_tracking(thread_id,scope_id,accepted_order) WHERE checked_at IS NULL;
-- Recoverable plans must not gain tools; terminal plans remain historical facts.
UPDATE runtime.turns SET config=jsonb_set(config,'{capabilities}',
 COALESCE(NULLIF(config->'capabilities','null'),'{}'::jsonb) ||
 jsonb_build_object('disabled',COALESCE(NULLIF(config->'capabilities'->'disabled','null'),'[]'::jsonb)||'["input_tracking"]'::jsonb))
WHERE state IN ('running','waiting')
 AND NOT (COALESCE(NULLIF(config->'capabilities'->'disabled','null'),'[]'::jsonb) ? 'input_tracking');
