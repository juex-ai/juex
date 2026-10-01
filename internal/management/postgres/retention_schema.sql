DROP TABLE management.outbox;
CREATE INDEX audit_retention ON management.audit(created_at,id);
CREATE INDEX identity_audit_retention ON management.identity_audit(created_at,id);
CREATE INDEX operator_audit_retention ON management.operator_audit(created_at,id);
