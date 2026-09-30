ALTER TABLE management.memberships ADD COLUMN removal_epoch bigint NOT NULL DEFAULT 1 CHECK (removal_epoch > 0);
