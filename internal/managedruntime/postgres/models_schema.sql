ALTER TABLE runtime.turns ADD COLUMN model_index integer NOT NULL DEFAULT 0 CHECK(model_index>=0);
