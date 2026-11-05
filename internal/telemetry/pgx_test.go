package telemetry

import "testing"

func TestSummarize(t *testing.T) {
	for sql, want := range map[string]string{
		`SELECT COALESCE(SUM(amount), 0)::bigint FROM entries WHERE account_id = $1`: "SELECT entries",
		`INSERT INTO entries (account_id, transfer_id, amount) VALUES ($1, $2, $3)`:  "INSERT entries",
		"\n\t\tselect id from transfers where created_at > $1":                       "SELECT transfers",
		`begin`: "BEGIN ",
	} {
		op, table := summarize(sql)
		if got := op + " " + table; got != want {
			t.Errorf("summarize(%q) = %q, want %q", sql, got, want)
		}
	}
}
