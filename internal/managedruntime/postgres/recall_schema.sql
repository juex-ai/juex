CREATE TABLE runtime.memory_recall (
 input_id uuid PRIMARY KEY REFERENCES runtime.inputs(id) ON DELETE CASCADE,
 activation_epoch bigint NOT NULL,
 snapshot jsonb NOT NULL DEFAULT '{"state":"preparing"}'
);
