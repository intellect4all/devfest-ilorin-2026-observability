// Command loadgen sends a steady stream of transfers so there is always
// something on the dashboard and in Jaeger. Ctrl-C to stop.
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
	"sync"
	"sync/atomic"
	"time"
)

func main() {
	target := flag.String("url", "http://localhost:8080/transfers", "transfers endpoint")
	workers := flag.Int("c", 4, "concurrent workers")
	accounts := flag.Int("accounts", 1000, "pick accounts from acct-00001..acct-N")
	pause := flag.Duration("pause", 200*time.Millisecond, "pause between requests, per worker")
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	client := &http.Client{Timeout: 10 * time.Second}
	var sent, ok, rejected, failed5xx, errs atomic.Int64

	var wg sync.WaitGroup
	for i := 0; i < *workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for ctx.Err() == nil {
				from, to := rand.IntN(*accounts)+1, rand.IntN(*accounts)+1
				if from == to {
					continue
				}
				body, _ := json.Marshal(map[string]any{
					"from": acct(from), "to": acct(to), "amount": 100 + rand.Int64N(4900),
				})
				req, err := http.NewRequestWithContext(ctx, http.MethodPost, *target, bytes.NewReader(body))
				if err != nil {
					return
				}
				req.Header.Set("Content-Type", "application/json")
				req.Header.Set("Idempotency-Key", fmt.Sprintf("load-%d-%d", time.Now().UnixNano(), rand.Uint32()))

				sent.Add(1)
				resp, err := client.Do(req)
				switch {
				case err != nil && ctx.Err() != nil:
					return // Ctrl-C, not a failure
				case err != nil:
					errs.Add(1)
				case resp.StatusCode >= 500:
					failed5xx.Add(1)
				case resp.StatusCode >= 400:
					rejected.Add(1)
				default:
					ok.Add(1)
				}
				if resp != nil {
					resp.Body.Close()
				}

				select {
				case <-ctx.Done():
				case <-time.After(*pause):
				}
			}
		}()
	}

	report := func() {
		fmt.Printf("sent=%d ok=%d 4xx=%d 5xx=%d errors=%d\n",
			sent.Load(), ok.Load(), rejected.Load(), failed5xx.Load(), errs.Load())
	}
	go func() {
		t := time.NewTicker(5 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				report()
			}
		}
	}()

	fmt.Printf("loadgen: %d workers -> %s (Ctrl-C to stop)\n", *workers, *target)
	wg.Wait()
	report()
}

func acct(n int) string { return fmt.Sprintf("acct-%05d", n) }
