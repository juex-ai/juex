ALTER TABLE runtime.inputs ADD COLUMN images jsonb NOT NULL DEFAULT '[]'::jsonb
  CHECK (jsonb_typeof(images) = 'array' AND jsonb_array_length(images) <= 8);
