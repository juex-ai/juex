-- Progress fencing and reads use the immutable request generation without
-- decompressing the full model request. The Turn can advance after compaction.
ALTER TABLE runtime.attempts ADD COLUMN request_generation bigint
    GENERATED ALWAYS AS ((request->>'generation')::bigint) STORED;
