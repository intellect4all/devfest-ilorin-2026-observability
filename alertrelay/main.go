// Command alertrelay turns Grafana alert webhooks into notifications a person
// can read on a phone, and publishes them to ntfy. Grafana's own webhook body
// is raw JSON; nobody should be paged with that.
//
//	POST /grafana   Grafana webhook contact point
//	POST /test      send a sample alert (make alert-test)
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"devfest/internal/httpx"
)

// webhook is the part of Grafana's webhook payload we use.
type webhook struct {
	Status string  `json:"status"`
	Alerts []alert `json:"alerts"`
}

type alert struct {
	Status      string            `json:"status"` // firing or resolved
	Labels      map[string]string `json:"labels"`
	Annotations map[string]string `json:"annotations"`
	StartsAt    time.Time         `json:"startsAt"`
	EndsAt      time.Time         `json:"endsAt"`
}

// message is an ntfy JSON publish request.
type message struct {
	Topic    string   `json:"topic"`
	Title    string   `json:"title"`
	Message  string   `json:"message"`
	Priority int      `json:"priority"`
	Tags     []string `json:"tags"`
	Click    string   `json:"click,omitempty"`
}

type relay struct {
	ntfyURL, topic, token, clickURL string
	http                            *http.Client
	loc                             *time.Location
}

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil)).With("service", "alertrelay")
	slog.SetDefault(log)

	loc, err := time.LoadLocation(httpx.Env("TZ_DISPLAY", "Africa/Lagos"))
	if err != nil {
		loc = time.UTC
	}
	r := &relay{
		ntfyURL:  strings.TrimRight(httpx.Env("NTFY_URL", "https://ntfy.sh"), "/"),
		topic:    os.Getenv("NTFY_TOPIC"),
		token:    os.Getenv("NTFY_TOKEN"),
		clickURL: os.Getenv("ALERT_CLICK_URL"),
		http:     &http.Client{Timeout: 10 * time.Second},
		loc:      loc,
	}
	if r.topic == "" {
		log.Error("NTFY_TOPIC is not set: copy .env.example to .env")
		os.Exit(1)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /grafana", r.grafana)
	mux.HandleFunc("POST /test", r.test)
	mux.HandleFunc("GET /healthz", httpx.Healthz)
	httpx.Serve(":"+httpx.Env("PORT", "8090"), mux, ":"+httpx.Env("ADMIN_PORT", "7090"), http.NotFoundHandler())
}

func (r *relay) grafana(w http.ResponseWriter, req *http.Request) {
	var hook webhook
	if err := json.NewDecoder(req.Body).Decode(&hook); err != nil {
		httpx.Error(w, http.StatusBadRequest, "not a Grafana webhook")
		return
	}
	for _, a := range hook.Alerts {
		m := r.format(a)
		if err := r.publish(req.Context(), m); err != nil {
			slog.Error("publishing to ntfy failed", "title", m.Title, "err", err)
			httpx.Error(w, http.StatusBadGateway, err.Error()) // Grafana retries
			return
		}
		slog.Info("alert relayed", "status", a.Status, "alertname", a.Labels["alertname"], "title", m.Title)
	}
	w.WriteHeader(http.StatusNoContent)
}

// test sends a sample alert through the same formatting as a real one.
func (r *relay) test(w http.ResponseWriter, req *http.Request) {
	m := r.format(alert{
		Status:      "firing",
		Labels:      map[string]string{"alertname": "Test alert from make alert-test", "service": "transfers", "severity": "page"},
		Annotations: map[string]string{"summary": "If you can read this on your phone, alerts will reach you during the talk."},
		StartsAt:    time.Now(),
	})
	if err := r.publish(req.Context(), m); err != nil {
		httpx.Error(w, http.StatusBadGateway, err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]string{"sent": m.Title, "to": r.ntfyURL + "/" + r.topic})
}

// format builds the phone notification: a title that says what is wrong,
// one or two plain sentences, and a priority and icon that match.
func (r *relay) format(a alert) message {
	name := a.Labels["alertname"]
	m := message{Topic: r.topic, Click: r.clickURL}
	summary := a.Annotations["summary"]
	if a.Status == "resolved" {
		m.Title = "Resolved: " + name
		m.Priority = 3
		m.Tags = []string{"white_check_mark"}
		took := ""
		if !a.EndsAt.IsZero() && a.EndsAt.After(a.StartsAt) {
			took = fmt.Sprintf(" after %s", a.EndsAt.Sub(a.StartsAt).Round(time.Second))
		}
		m.Message = fmt.Sprintf("Back to normal at %s%s.\n%s", a.EndsAt.In(r.loc).Format("15:04:05"), took, summary)
	} else {
		m.Title = name
		m.Priority = 5
		m.Tags = []string{"rotating_light"}
		m.Message = fmt.Sprintf("%s\nFiring since %s (%s).", summary, a.StartsAt.In(r.loc).Format("15:04:05"), a.Labels["service"])
	}
	return m
}

func (r *relay) publish(ctx context.Context, m message) error {
	body, err := json.Marshal(m)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.ntfyURL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if r.token != "" {
		req.Header.Set("Authorization", "Bearer "+r.token)
	}
	resp, err := r.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("ntfy answered %s", resp.Status)
	}
	return nil
}
