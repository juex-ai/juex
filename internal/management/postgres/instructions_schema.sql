ALTER TABLE management.agents ADD COLUMN dynamic_instructions jsonb NOT NULL DEFAULT '{"enabled":false,"global_path":""}';
