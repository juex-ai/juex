ALTER TABLE management.agents ADD COLUMN worker_depth integer NOT NULL DEFAULT 1 CHECK(worker_depth BETWEEN 1 AND 2);
