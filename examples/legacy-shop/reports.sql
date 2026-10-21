-- The old reporting scripts, written against Shop 1.0.

-- Customer list
SELECT id, name, email FROM customers ORDER BY id;

-- Customer count
SELECT count(*) AS customers FROM customers;

-- One customer, every column
SELECT * FROM customers WHERE id = 2;

-- Customers by initial
SELECT name FROM customers WHERE name LIKE 'A%' ORDER BY name;

-- Orders with their customer
SELECT o.id, c.name, o.total
  FROM orders o JOIN customers c ON c.id = o.customer_id
 ORDER BY o.id;

-- Orders by status
SELECT status, count(*) AS n, sum(total) AS amount
  FROM orders GROUP BY status ORDER BY status;

-- Top spenders
SELECT c.name, round(sum(o.total), 2) AS spent
  FROM customers c JOIN orders o ON o.customer_id = c.id
 GROUP BY c.id ORDER BY spent DESC;

-- Spring orders
SELECT * FROM orders WHERE placed >= '2026-03-01' ORDER BY placed;

-- Price list
SELECT name, price FROM products ORDER BY price DESC;

-- Monthly revenue
SELECT strftime('%Y-%m', placed) AS month, sum(total) AS revenue
  FROM orders GROUP BY month ORDER BY month;

-- Customers without a fax number
SELECT email FROM customers WHERE fax IS NULL ORDER BY email;

-- Largest paid order
SELECT max(total) AS largest FROM orders WHERE status = 'paid';
