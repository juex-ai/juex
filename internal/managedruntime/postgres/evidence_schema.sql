CREATE TABLE runtime.memory_evidence (
 input_id uuid PRIMARY KEY REFERENCES runtime.inputs(id) ON DELETE CASCADE,
 attempted_at timestamptz NOT NULL DEFAULT '-infinity'
);
