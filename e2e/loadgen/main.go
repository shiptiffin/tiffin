//go:build e2e

// Command loadgen is the steady load of the edge e2e test: workers send GET
// requests to one HTTPS URL (half over HTTP/1.1, half over HTTP/2, keeping
// connections alive) until a stop file appears, then it writes what it saw
// as JSON: requests, failures with the first errors, the slowest request
// and the longest stretch without a successful response.
package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

type result struct {
	Requests     int      `json:"requests"`
	OK           int      `json:"ok"`
	Failed       int      `json:"failed"`
	Errors       []string `json:"errors,omitempty"`
	MaxLatencyMs float64  `json:"maxLatencyMs"`
	MaxGapMs     float64  `json:"maxGapMs"`
	Seconds      float64  `json:"seconds"`
}

func main() {
	url := flag.String("url", "", "URL to request")
	addr := flag.String("addr", "127.0.0.1:8443", "address to connect to")
	caFile := flag.String("ca", "", "CA certificate (PEM)")
	want := flag.String("want", "", "a successful response's body contains this")
	workers := flag.Int("c", 8, "workers")
	pause := flag.Duration("pause", 5*time.Millisecond, "pause between a worker's requests")
	stop := flag.String("stop", "", "stop once this file exists")
	out := flag.String("out", "", "write the result here")
	flag.Parse()

	pem, err := os.ReadFile(*caFile)
	if err != nil {
		log.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(pem)
	transport := func(h2 bool) *http.Transport {
		d := &net.Dialer{Timeout: 10 * time.Second}
		return &http.Transport{
			TLSClientConfig:     &tls.Config{RootCAs: pool},
			ForceAttemptHTTP2:   h2,
			DialContext:         func(ctx context.Context, n, _ string) (net.Conn, error) { return d.DialContext(ctx, n, *addr) },
			MaxIdleConnsPerHost: 4,
		}
	}
	var (
		mu     sync.Mutex
		res    result
		lastOK = time.Now()
	)
	began := time.Now()
	done := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < *workers; i++ {
		c := &http.Client{Timeout: 60 * time.Second, Transport: transport(i%2 == 1)}
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-done:
					return
				default:
				}
				t0 := time.Now()
				var failure string
				resp, err := c.Get(*url)
				if err != nil {
					failure = err.Error()
				} else {
					body, err := io.ReadAll(resp.Body)
					resp.Body.Close()
					switch {
					case err != nil:
						failure = "body: " + err.Error()
					case resp.StatusCode != 200 || !strings.Contains(string(body), *want):
						failure = fmt.Sprintf("HTTP %d %.120q", resp.StatusCode, body)
					}
				}
				now := time.Now()
				mu.Lock()
				res.Requests++
				res.MaxLatencyMs = max(res.MaxLatencyMs, float64(now.Sub(t0).Microseconds())/1000)
				if failure == "" {
					res.OK++
					res.MaxGapMs = max(res.MaxGapMs, float64(now.Sub(lastOK).Microseconds())/1000)
					lastOK = now
				} else {
					res.Failed++
					if len(res.Errors) < 20 {
						res.Errors = append(res.Errors, now.Format("15:04:05.000")+" "+failure)
					}
				}
				mu.Unlock()
				time.Sleep(*pause)
			}
		}()
	}
	for {
		if _, err := os.Stat(*stop); err == nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	close(done)
	wg.Wait()
	mu.Lock()
	res.Seconds = time.Since(began).Seconds()
	raw, _ := json.MarshalIndent(res, "", "  ")
	mu.Unlock()
	if err := os.WriteFile(*out+".tmp", raw, 0o644); err != nil {
		log.Fatal(err)
	}
	if err := os.Rename(*out+".tmp", *out); err != nil {
		log.Fatal(err)
	}
}
