// Command loadgen sends transfers at a fixed rate (open loop): a new request
// starts every 1/rate seconds whether or not earlier ones have finished, so
// a slow server cannot slow the traffic down, just like real users.
//
// A share of the traffic (-hot) pays out of one "settlement wallet",
// acct-00001, which is how payout platforms really look: most money leaves
// from a few pool accounts.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"math/rand/v2"
	"net/http"
	"os"
	"os/signal"
	"slices"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

type stats struct {
	mu        sync.Mutex
	latencies []time.Duration
	codes     map[string]int
}

func (s *stats) record(code string, d time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.codes[code]++
	if code != "err" {
		s.latencies = append(s.latencies, d)
	}
}

// flush prints the window's counts and latency percentiles, then resets.
func (s *stats) flush(label string, inflight int64) {
	s.mu.Lock()
	lat, codes := s.latencies, s.codes
	s.latencies, s.codes = nil, map[string]int{}
	s.mu.Unlock()

	slices.Sort(lat)
	pct := func(p float64) time.Duration {
		if len(lat) == 0 {
			return 0
		}
		return lat[min(len(lat)-1, int(float64(len(lat))*p))].Round(time.Millisecond)
	}
	fmt.Printf("%s %s done=%d 2xx=%d 4xx=%d 5xx=%d err=%d inflight=%d p50=%s p99=%s\n",
		time.Now().Format("15:04:05"), label, len(lat), codes["2xx"], codes["4xx"], codes["5xx"], codes["err"],
		inflight, pct(0.50), pct(0.99))
}

func main() {
	target := flag.String("url", "http://localhost:8080/transfers", "transfers endpoint")
	rate := flag.Float64("rate", 20, "requests per second")
	duration := flag.Duration("duration", 0, "stop after this long (0 = until Ctrl-C)")
	hot := flag.Float64("hot", 0.9, "share of transfers sent from the settlement wallet acct-00001")
	accounts := flag.Int("accounts", 1000, "other senders and receivers are acct-00002..acct-N")
	replay := flag.Float64("replay", 0.02, "share of requests that retry an earlier Idempotency-Key")
	timeout := flag.Duration("timeout", 10*time.Second, "client timeout")
	label := flag.String("label", "load", "label printed on each stats line")
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if *duration > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, *duration)
		defer cancel()
	}

	client := &http.Client{
		Timeout:   *timeout,
		Transport: &http.Transport{MaxIdleConnsPerHost: 1000, MaxConnsPerHost: 0},
	}
	st := &stats{codes: map[string]int{}}
	var inflight atomic.Int64
	var recentKeys [256]atomic.Value // ring of keys to retry

	send := func(n uint64) {
		defer inflight.Add(-1)
		from := "acct-00001"
		if rand.Float64() >= *hot {
			from = acct(2 + rand.IntN(*accounts-1))
		}
		to := acct(2 + rand.IntN(*accounts-1))
		if to == from {
			to = acct(*accounts + 1)
		}

		key := uuid() // upper case, like the iOS SDK sends them
		if k, ok := recentKeys[rand.IntN(len(recentKeys))].Load().(string); ok && rand.Float64() < *replay {
			key = k
		} else {
			recentKeys[n%uint64(len(recentKeys))].Store(key)
		}

		body, _ := json.Marshal(map[string]any{"from": from, "to": to, "amount": 100 + rand.Int64N(4900)})
		req, err := http.NewRequestWithContext(context.WithoutCancel(ctx), http.MethodPost, *target, bytes.NewReader(body))
		if err != nil {
			return
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Idempotency-Key", key)

		start := time.Now()
		resp, err := client.Do(req)
		code := "err"
		if err == nil {
			code = fmt.Sprintf("%dxx", resp.StatusCode/100)
			resp.Body.Close()
		}
		st.record(code, time.Since(start))
	}

	fmt.Printf("loadgen: %.0f req/s, %.0f%% from acct-00001 -> %s\n", *rate, *hot*100, *target)
	tick := time.NewTicker(time.Duration(float64(time.Second) / *rate))
	defer tick.Stop()
	report := time.NewTicker(5 * time.Second)
	defer report.Stop()

	var wg sync.WaitGroup
	for n := uint64(0); ; n++ {
		select {
		case <-ctx.Done():
			wg.Wait()
			st.flush(*label, 0)
			return
		case <-report.C:
			st.flush(*label, inflight.Load())
		case <-tick.C:
			inflight.Add(1)
			wg.Add(1)
			go func() { defer wg.Done(); send(n) }()
		}
	}
}

func acct(n int) string { return fmt.Sprintf("acct-%05d", n) }

// uuid returns a random version-4 UUID in upper case.
func uuid() string {
	a, b := rand.Uint64(), rand.Uint64()
	a = a&^0xf000 | 0x4000     // version 4
	b = b&^(0xc<<60) | 0x8<<60 // RFC 4122 variant
	return fmt.Sprintf("%08X-%04X-%04X-%04X-%012X", a>>32, a>>16&0xffff, a&0xffff, b>>48, b&0xffffffffffff)
}
