ALTER TABLE execution.hosted
    ADD COLUMN storage_identity uuid NOT NULL,
    ADD COLUMN project_id bigint GENERATED ALWAYS AS IDENTITY UNIQUE CHECK(project_id>0 AND project_id<=4294967295),
    ADD COLUMN workspace_bytes bigint NOT NULL CHECK(workspace_bytes>=16777216 AND workspace_bytes%512=0),
    ADD COLUMN workspace_inodes bigint NOT NULL CHECK(workspace_inodes>=64),
    ADD COLUMN provisioned boolean NOT NULL DEFAULT false;
