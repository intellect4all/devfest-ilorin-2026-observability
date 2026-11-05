package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// A Grafana webhook must become a readable phone notification, never raw JSON.
func TestGrafanaWebhookBecomesReadableNotification(t *testing.T) {
	var got []message
	ntfy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var m message
		_ = json.NewDecoder(r.Body).Decode(&m)
		got = append(got, m)
	}))
	defer ntfy.Close()

	r := &relay{ntfyURL: ntfy.URL, topic: "t", http: ntfy.Client(), loc: time.UTC}
	start := time.Date(2026, 11, 7, 11, 14, 5, 0, time.UTC)
	body := `{"status":"firing","alerts":[
	 {"status":"firing","labels":{"alertname":"Transfers p99 latency above 300 ms","service":"transfers"},
	  "annotations":{"summary":"p99 latency of POST /transfers is 1.84s (SLO 300ms)."},"startsAt":"` + start.Format(time.RFC3339) + `"},
	 {"status":"resolved","labels":{"alertname":"Transfers p99 latency above 300 ms","service":"transfers"},
	  "annotations":{"summary":"p99 latency of POST /transfers is 120ms (SLO 300ms)."},
	  "startsAt":"` + start.Format(time.RFC3339) + `","endsAt":"` + start.Add(95*time.Second).Format(time.RFC3339) + `"}]}`

	rec := httptest.NewRecorder()
	r.grafana(rec, httptest.NewRequest(http.MethodPost, "/grafana", strings.NewReader(body)))
	if rec.Code != http.StatusNoContent {
		b, _ := io.ReadAll(rec.Body)
		t.Fatalf("status %d: %s", rec.Code, b)
	}
	if len(got) != 2 {
		t.Fatalf("published %d messages, want 2", len(got))
	}
	fire, res := got[0], got[1]
	if fire.Title != "Transfers p99 latency above 300 ms" || fire.Priority != 5 || fire.Tags[0] != "rotating_light" {
		t.Errorf("firing message = %+v", fire)
	}
	if !strings.Contains(fire.Message, "1.84s") || !strings.Contains(fire.Message, "Firing since 11:14:05") {
		t.Errorf("firing body = %q", fire.Message)
	}
	if res.Title != "Resolved: Transfers p99 latency above 300 ms" || res.Priority != 3 || !strings.Contains(res.Message, "after 1m35s") {
		t.Errorf("resolved message = %+v", res)
	}
	for _, m := range got {
		if strings.Contains(m.Message, "{") {
			t.Errorf("message body looks like JSON: %q", m.Message)
		}
	}
}
