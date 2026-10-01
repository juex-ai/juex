-- Offline deployment operators have no tenant user identity.
ALTER TABLE execution.audit ALTER COLUMN actor_id DROP NOT NULL;
ALTER TABLE execution.audit ADD CONSTRAINT audit_actor CHECK(actor_id IS NOT NULL OR action='recovery.device_revoked');
