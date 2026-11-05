-- Seeds the ledger with :rows entries spread over 50,000 accounts
-- (acct-00001 .. acct-50000). Every account ends up with a positive balance.
-- Usage: psql -v rows=3000000 -f seed.sql   (or: make seed ROWS=3000000)
\timing on

CREATE TABLE IF NOT EXISTS entries (
  id          BIGSERIAL PRIMARY KEY,
  account_id  TEXT        NOT NULL,
  transfer_id TEXT        NOT NULL,
  amount      BIGINT      NOT NULL,
  memo        TEXT        NOT NULL DEFAULT '',
  created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

INSERT INTO entries (account_id, transfer_id, amount, memo)
SELECT 'acct-' || lpad((1 + (g % 50000))::text, 5, '0'),
       'seed-' || g,
       CASE WHEN g % 3 = 0 THEN -5000 ELSE 10000 END,
       'opening balance migration from legacy core banking system'
FROM generate_series(1, :rows) AS g;

ANALYZE entries;
SELECT count(*) AS entries, pg_size_pretty(pg_total_relation_size('entries')) AS size FROM entries;
