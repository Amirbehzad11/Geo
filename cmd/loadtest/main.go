package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"flag"
	"fmt"
	"io"
	"log"
	"math"
	"net"
	"net/http"
	"os"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

type result struct {
	status  int
	latency time.Duration
	err     bool
	errText string
}

type cityPair struct {
	startLat float64
	startLng float64
	endLat   float64
	endLng   float64
}

var routePairs = []cityPair{
	{35.6892, 51.3890, 35.7219, 51.3347}, // Tehran
	{32.6539, 51.6660, 32.6860, 51.6880}, // Isfahan
	{36.2605, 59.6168, 36.3107, 59.5757}, // Mashhad
	{29.5918, 52.5837, 29.6259, 52.5311}, // Shiraz
	{38.0800, 46.2919, 38.0668, 46.3597}, // Tabriz
	{31.3183, 48.6706, 31.3374, 48.7620}, // Ahvaz
	{34.6416, 50.8746, 34.6649, 50.8501}, // Qom
	{35.8327, 50.9915, 35.8016, 51.0116}, // Karaj
}

func main() {
	baseURL := flag.String("base", "http://127.0.0.1:8080", "Base URL")
	scenario := flag.String("scenario", "gps", "gps, route, or mixed")
	concurrency := flag.Int("c", 100, "Concurrent virtual users")
	duration := flag.Duration("duration", 30*time.Second, "Test duration")
	timeout := flag.Duration("timeout", 5*time.Second, "Per-request timeout")
	think := flag.Duration("think", 0, "Delay between requests per virtual user")
	startTripID := flag.Int64("trip-start", 9_000_000_000, "First generated trip_id")
	flag.Parse()

	if *concurrency <= 0 || *duration <= 0 {
		log.Fatal("concurrency and duration must be positive")
	}

	client := newHTTPClient(*timeout, *concurrency)
	results := make(chan result, *concurrency*2048)
	deadline := time.Now().Add(*duration)

	var reqSeq int64
	var wg sync.WaitGroup
	started := time.Now()
	for workerID := 0; workerID < *concurrency; workerID++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			for {
				if time.Now().After(deadline) {
					return
				}

				seq := atomic.AddInt64(&reqSeq, 1)
				st := time.Now()
				status, err := runRequest(context.Background(), client, *baseURL, *scenario, *startTripID, seq, workerID)
				errText := ""
				if err != nil {
					errText = err.Error()
				}
				results <- result{status: status, latency: time.Since(st), err: err != nil, errText: errText}

				if *think > 0 {
					timer := time.NewTimer(*think)
					<-timer.C
				}
			}
		}(workerID)
	}

	wg.Wait()
	close(results)
	printSummary(*scenario, *concurrency, *duration, time.Since(started), results)
}

func newHTTPClient(timeout time.Duration, concurrency int) *http.Client {
	transport := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   2 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		MaxIdleConns:          concurrency * 4,
		MaxIdleConnsPerHost:   concurrency * 4,
		MaxConnsPerHost:       concurrency * 4,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   2 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		TLSClientConfig:       &tls.Config{MinVersion: tls.VersionTLS12},
	}
	return &http.Client{Transport: transport, Timeout: timeout}
}

func runRequest(ctx context.Context, client *http.Client, baseURL, scenario string, startTripID, seq int64, workerID int) (int, error) {
	switch scenario {
	case "gps":
		return postJSON(ctx, client, baseURL+"/gps/update", gpsBody(startTripID, seq, workerID))
	case "route":
		return postJSON(ctx, client, baseURL+"/route", routeBody(startTripID, seq, workerID))
	case "mixed":
		if seq%10 == 0 {
			return postJSON(ctx, client, baseURL+"/route", routeBody(startTripID, seq, workerID))
		}
		return postJSON(ctx, client, baseURL+"/gps/update", gpsBody(startTripID, seq, workerID))
	default:
		return 0, fmt.Errorf("unknown scenario %q", scenario)
	}
}

func postJSON(ctx context.Context, client *http.Client, url string, body []byte) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return resp.StatusCode, fmt.Errorf("status %d", resp.StatusCode)
	}
	return resp.StatusCode, nil
}

func gpsBody(startTripID, seq int64, workerID int) []byte {
	lat := 35.6892 + wave(seq+int64(workerID), 0.006)
	lng := 51.3890 + wave(seq*3+int64(workerID), 0.006)
	ts := time.Now().Unix()
	return []byte(fmt.Sprintf(
		`{"trip_id":%d,"lat":%.7f,"lng":%.7f,"timestamp":%d}`,
		startTripID+seq,
		lat,
		lng,
		ts,
	))
}

func routeBody(startTripID, seq int64, workerID int) []byte {
	seed := startTripID + seq
	pair := routePairs[int(seed)%len(routePairs)]
	offsetA := wave(seed+int64(workerID), 0.0012)
	offsetB := wave(seed*7+int64(workerID), 0.0012)
	return []byte(fmt.Sprintf(
		`{"trip_id":%d,"start_lat":%.7f,"start_lng":%.7f,"end_lat":%.7f,"end_lng":%.7f,"mode":"car","alternatives":1}`,
		startTripID+seq,
		pair.startLat+offsetA,
		pair.startLng+offsetB,
		pair.endLat-offsetB,
		pair.endLng-offsetA,
	))
}

func wave(seed int64, scale float64) float64 {
	return math.Sin(float64(seed%100000)*12.9898) * scale
}

func printSummary(scenario string, concurrency int, requested, elapsed time.Duration, results <-chan result) {
	statusCounts := map[int]int{}
	errorCounts := map[string]int{}
	var latencies []time.Duration
	var total, success, failed int

	for r := range results {
		total++
		statusCounts[r.status]++
		if r.err {
			failed++
			errorCounts[r.errText]++
		} else {
			success++
		}
		latencies = append(latencies, r.latency)
	}

	sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })

	fmt.Fprintf(os.Stdout, "scenario=%s concurrency=%d requested_duration=%s elapsed=%s\n", scenario, concurrency, requested, elapsed.Round(time.Millisecond))
	fmt.Fprintf(os.Stdout, "total=%d success=%d failed=%d rps=%.2f success_rps=%.2f\n", total, success, failed, float64(total)/elapsed.Seconds(), float64(success)/elapsed.Seconds())
	fmt.Fprintf(os.Stdout, "status=%v\n", statusCounts)
	if len(errorCounts) > 0 {
		fmt.Fprintf(os.Stdout, "errors=%v\n", topErrors(errorCounts, 5))
	}
	if len(latencies) == 0 {
		return
	}
	fmt.Fprintf(os.Stdout, "latency_min=%s p50=%s p90=%s p95=%s p99=%s max=%s\n",
		latencies[0].Round(time.Microsecond),
		percentile(latencies, 50).Round(time.Microsecond),
		percentile(latencies, 90).Round(time.Microsecond),
		percentile(latencies, 95).Round(time.Microsecond),
		percentile(latencies, 99).Round(time.Microsecond),
		latencies[len(latencies)-1].Round(time.Microsecond),
	)
}

func topErrors(counts map[string]int, limit int) map[string]int {
	type item struct {
		key   string
		count int
	}
	items := make([]item, 0, len(counts))
	for key, count := range counts {
		items = append(items, item{key: key, count: count})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].count > items[j].count })
	if len(items) > limit {
		items = items[:limit]
	}
	out := make(map[string]int, len(items))
	for _, it := range items {
		out[it.key] = it.count
	}
	return out
}

func percentile(sorted []time.Duration, p float64) time.Duration {
	if len(sorted) == 0 {
		return 0
	}
	rank := (p / 100) * float64(len(sorted)-1)
	lo := int(math.Floor(rank))
	hi := int(math.Ceil(rank))
	if lo == hi {
		return sorted[lo]
	}
	frac := rank - float64(lo)
	return time.Duration(float64(sorted[lo])*(1-frac) + float64(sorted[hi])*frac)
}
