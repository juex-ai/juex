ALTER TABLE management.agents ADD COLUMN capabilities jsonb NOT NULL DEFAULT '{"disabled":[]}';
