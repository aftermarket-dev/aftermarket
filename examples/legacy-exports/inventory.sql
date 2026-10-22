-- The legacy app's inventory database, as a dump. A database file works the
-- same way: sqlite3 inventory.db < inventory.sql, then db: inventory.db.
CREATE TABLE products (
  sku          TEXT PRIMARY KEY,
  name         TEXT NOT NULL,
  price        REAL,
  stock        INTEGER,
  discontinued INTEGER NOT NULL DEFAULT 0,
  photo        BLOB
);
INSERT INTO products VALUES
  ('BK-001', 'Notes on the Analytical Engine', 24.5, 12, 0, x'89504e47'),
  ('BK-002', 'COBOL for Beginners', 19.99, 0, 1, NULL),
  ('MG-010', 'Difference Engine magnet', 3, 140, 0, NULL);
