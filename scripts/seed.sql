-- Seed data for the workshop. Run with: make seed ROWS=... HISTORY=...
--
--   entries    :rows ledger entries spread over 50,000 accounts. The bigger the
--              table, the slower the balance query once bug B drops its index.
--              acct-00001 is the settlement wallet that most payouts leave from,
--              so it gets a huge opening balance.
--   transfers  :history completed transfers from the last 24 hours. The
--              transfers service loads them at start-up to recognise retried
--              Idempotency-Keys; bug A scans all of them on every request.
--
-- The services create the tables; this script only fills them, so run it
-- after `make up`.
\set ON_ERROR_STOP on
\timing on

TRUNCATE entries, transfers;

-- Bulk-load without the index, then build it once: much faster.
DROP INDEX IF EXISTS idx_entries_account_id;

INSERT INTO entries (account_id, transfer_id, amount, memo)
SELECT 'acct-' || lpad((1 + (g % 50000))::text, 5, '0'),
       'seed-' || g,
       CASE WHEN g % 3 = 0 THEN -5000 ELSE 10000 END,
       'opening balance migration from legacy core banking system'
FROM generate_series(1, :rows) AS g;

INSERT INTO entries (account_id, transfer_id, amount, memo)
VALUES ('acct-00001', 'seed-settlement-float', 1000000000000000, 'settlement wallet float');

CREATE INDEX idx_entries_account_id ON entries (account_id);

INSERT INTO transfers (id, idempotency_key, from_account, to_account, amount, status, provider_ref, created_at)
SELECT 'trf_seed' || lpad(g::text, 10, '0'),
       upper(md5(g::text)::uuid::text),
       'acct-' || lpad((1 + (g % 1000))::text, 5, '0'),
       'acct-' || lpad((2 + (g % 997))::text, 5, '0'),
       100 + (g % 4900),
       'completed',
       'PRV-SEED' || g,
       now() - interval '23 hours' * (1 - g::float / :history)
FROM generate_series(1, :history) AS g;

ANALYZE entries;
ANALYZE transfers;

SELECT (SELECT count(*) FROM entries) AS entries,
       pg_size_pretty(pg_total_relation_size('entries')) AS entries_size,
       (SELECT count(*) FROM transfers) AS transfers;
