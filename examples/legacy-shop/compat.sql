-- Compatibility views from map.yaml, written by aftermarket schema compat.
-- They show the old tables, read from the new schema, for old queries that read.
-- Review them, then apply to the new database, for example: sqlite3 new.db < compat.sql

BEGIN;

DROP VIEW IF EXISTS "customers";
CREATE VIEW "customers" ("id", "name", "email", "fax", "created") AS
  SELECT
    "id",
    first_name || ' ' || last_name,
    "email",
    NULL,
    created_at
  FROM "accounts"
  WHERE deleted_at IS NULL;

DROP VIEW IF EXISTS "orders";
CREATE VIEW "orders" ("id", "customer_id", "total", "status", "placed") AS
  SELECT
    "id",
    account_id,
    total_cents / 100.0,
    state,
    placed_at
  FROM "purchases";

COMMIT;
