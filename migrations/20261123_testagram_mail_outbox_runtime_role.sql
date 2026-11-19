-- Repair mail-outbox runtime grants on installations where the original
-- migration ran before the dedicated application group role existed.
BEGIN;

DO $role$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname='testagram_app') THEN
    EXECUTE 'CREATE ROLE testagram_app NOLOGIN';
  END IF;
END;
$role$;

GRANT USAGE ON SCHEMA testagram_mail TO testagram_app;
GRANT SELECT,INSERT,UPDATE ON TABLE testagram_mail.outbox TO testagram_app;
GRANT USAGE,SELECT ON SEQUENCE testagram_mail.outbox_id_seq TO testagram_app;

DO $verify$
BEGIN
  IF NOT has_schema_privilege('testagram_app','testagram_mail','USAGE')
     OR NOT has_table_privilege('testagram_app','testagram_mail.outbox','SELECT')
     OR NOT has_table_privilege('testagram_app','testagram_mail.outbox','INSERT')
     OR NOT has_table_privilege('testagram_app','testagram_mail.outbox','UPDATE')
     OR NOT has_sequence_privilege('testagram_app','testagram_mail.outbox_id_seq','USAGE')
     OR NOT has_sequence_privilege('testagram_app','testagram_mail.outbox_id_seq','SELECT') THEN
    RAISE EXCEPTION 'mail outbox application role grants are incomplete after repair';
  END IF;
END;
$verify$;

COMMIT;
