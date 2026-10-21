-- Shop 2.0: tables renamed, names split, fax dropped, totals in cents and
-- soft deletes. The sample data is 1.0's, migrated, plus one deleted account.
CREATE TABLE accounts (
  id         INTEGER PRIMARY KEY,
  first_name TEXT NOT NULL,
  last_name  TEXT NOT NULL,
  email      TEXT,
  created_at TEXT,
  deleted_at TEXT
);

CREATE TABLE purchases (
  id          INTEGER PRIMARY KEY,
  account_id  INTEGER NOT NULL REFERENCES accounts(id),
  total_cents INTEGER NOT NULL,
  state       TEXT NOT NULL DEFAULT 'open',
  placed_at   TEXT
);

CREATE TABLE products (
  id    INTEGER PRIMARY KEY,
  name  TEXT NOT NULL,
  price REAL,
  sku   TEXT
);

CREATE INDEX purchases_by_account ON purchases(account_id);
CREATE UNIQUE INDEX accounts_email ON accounts(email);

INSERT INTO accounts VALUES
  (1, 'Ada',     'Lovelace', 'ada@example.com',   '2025-11-02', NULL),
  (2, 'Charles', 'Babbage',  'cb@example.com',    '2025-12-14', NULL),
  (3, 'Grace',   'Hopper',   'grace@example.com', '2026-01-20', NULL),
  (4, 'Alan',    'Turing',   'alan@example.com',  '2026-02-09', NULL),
  (5, 'Test',    'Account',  'test@example.com',  '2026-05-01', '2026-05-02');

INSERT INTO purchases VALUES
  (1, 1, 3980,  'paid', '2026-01-05'),
  (2, 1, 12000, 'paid', '2026-02-11'),
  (3, 2, 1525,  'open', '2026-02-28'),
  (4, 3, 24999, 'paid', '2026-03-03'),
  (5, 4, 850,   'open', '2026-03-19'),
  (6, 3, 6410,  'paid', '2026-04-02');

INSERT INTO products VALUES
  (1, 'Notebook', 4.5, 'NB-01'),
  (2, 'Fountain pen', 39.8, 'FP-02'),
  (3, 'Desk lamp', 64.1, 'DL-03');
