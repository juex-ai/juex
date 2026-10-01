ALTER TABLE management.agents ADD COLUMN hooks jsonb NOT NULL DEFAULT '[]';
