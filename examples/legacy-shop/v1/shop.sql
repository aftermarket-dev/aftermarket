-- Shop 1.0: the schema the old reports were written against, with sample data.
CREATE TABLE customers (
  id      INTEGER PRIMARY KEY,
  name    TEXT NOT NULL,
  email   TEXT,
  fax     TEXT,
  created TEXT
);

CREATE TABLE orders (
  id          INTEGER PRIMARY KEY,
  customer_id INTEGER REFERENCES customers(id),
  total       REAL,
  status      TEXT,
  placed      TEXT
);

CREATE TABLE products (
  id    INTEGER PRIMARY KEY,
  name  TEXT NOT NULL,
  price REAL
);

CREATE INDEX orders_by_customer ON orders(customer_id);

INSERT INTO customers VALUES
  (1, 'Ada Lovelace',     'ada@example.com',   NULL,       '2025-11-02'),
  (2, 'Charles Babbage',  'cb@example.com',    NULL,       '2025-12-14'),
  (3, 'Grace Hopper',     'grace@example.com', NULL,       '2026-01-20'),
  (4, 'Alan Turing',      'alan@example.com',  NULL,       '2026-02-09');

INSERT INTO orders VALUES
  (1, 1, 39.8,   'paid', '2026-01-05'),
  (2, 1, 120.0,  'paid', '2026-02-11'),
  (3, 2, 15.25,  'open', '2026-02-28'),
  (4, 3, 249.99, 'paid', '2026-03-03'),
  (5, 4, 8.5,    'open', '2026-03-19'),
  (6, 3, 64.1,   'paid', '2026-04-02');

INSERT INTO products VALUES
  (1, 'Notebook', 4.5),
  (2, 'Fountain pen', 39.8),
  (3, 'Desk lamp', 64.1);
