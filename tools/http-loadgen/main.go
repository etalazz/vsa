// http-loadgen is a tiny, dependency-free HTTP load generator tailored for the VSA demo.
// It reuses HTTP connections (keep-alive) and supports concurrency so demo scripts run fast
// on Windows (Git Bash), Ubuntu (WSL), and macOS without relying on external tools.
//
// Modes:
//   - single: send N requests for a single key
//   - zipf:   approximate 80/20 skew (hot/cold) without PRNG: send hot key 4/5 of the time
//
// Usage examples:
//
//	http-loadgen -base=http://127.0.0.1:8080 -mode=single -key=alice -n=5000 -c=16
//	http-loadgen -base=http://127.0.0.1:8080 -mode=zipf -hot_key=hot-1 -cold_keys=50 -n=8000 -c=16
//
// Notes:
//   - Uses GET with one query parameter (default api_key). Keys are URL-encoded.
//   - Prints a one-line summary with duration and approximate throughput.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type modeType string

const (
	modeSingle  modeType = "single"
	modeZipf    modeType = "zipf"
	modeRelease modeType = "release"
)

func main() {
	var (
		base   = flag.String("base", "http://127.0.0.1:8080", "Base URL including scheme and host, e.g. http://127.0.0.1:8080")
		path   = flag.String("path", "/check", "Request path (e.g., /check)")
		param  = flag.String("param", "api_key", "Query parameter name for the key")
		modeS  = flag.String("mode", string(modeSingle), "Mode: single|zipf")
		key    = flag.String("key", "alice-key", "Key for single mode")
		hotKey = flag.String("hot_key", "hot-1", "Hot key for zipf mode")
		coldN  = flag.Int("cold_keys", 50, "Number of cold keys to round-robin in zipf mode")
		N      = flag.Int("n", 5000, "Total requests to send")
		conc   = flag.Int("c", 8, "Number of concurrent workers")
		// Deterministic skew: hotEvery=5 means 4/5 go to hot key, 1/5 to a cold key.
		hotEvery = flag.Int("hot_every", 5, "Zipf-like skew period (4 of this period go to hot; minimum 2)")
		// Timeouts & transport tuning
		timeout    = flag.Duration("timeout", 20*time.Second, "Overall timeout for the loadgen run")
		connIdle   = flag.Duration("idle_timeout", 30*time.Second, "HTTP idle connection timeout")
		maxIdle    = flag.Int("max_idle", 256, "Max idle connections total")
		maxIdlePer = flag.Int("max_idle_per_host", 256, "Max idle connections per host")
	)
	flag.Parse()

	m := modeType(strings.ToLower(*modeS))
	if m != modeSingle && m != modeZipf {
		fmt.Fprintf(os.Stderr, "unknown -mode=%s (want single|zipf)\n", *modeS)
		os.Exit(2)
	}
	if *N <= 0 || *conc <= 0 {
		fmt.Fprintln(os.Stderr, "-n and -c must be > 0")
		os.Exit(2)
	}
	if m == modeZipf {
		if *coldN <= 0 {
			fmt.Fprintln(os.Stderr, "-cold_keys must be > 0 in zipf mode")
			os.Exit(2)
		}
		if *hotEvery < 2 { // at least 1 hot : 1 cold
			*hotEvery = 2
		}
	}

	// Build base + path
	baseURL := strings.TrimRight(*base, "/")
	p := *path
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	fullPath := baseURL + p

	// Configure HTTP client with connection reuse
	tr := &http.Transport{
		Proxy:               http.ProxyFromEnvironment,
		MaxIdleConns:        *maxIdle,
		MaxIdleConnsPerHost: *maxIdlePer,
		IdleConnTimeout:     *connIdle,
	}
	client := &http.Client{Transport: tr, Timeout: 5 * time.Second}

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	start := time.Now()
	var done int64

	worker := func(id, count int) {
		defer atomic.AddInt64(&done, int64(count))
		for i := 0; i < count; i++ {
			select {
			case <-ctx.Done():
				return
			default:
			}
			var k string
			if m == modeSingle || m == modeRelease {
				k = *key
			} else {
				// 80/20-ish deterministic skew: (i+id)%hotEvery != 0 => hot key
				if ((i + id) % *hotEvery) != 0 {
					k = *hotKey
				} else {
					idx := ((i + id) % *coldN) + 1
					k = fmt.Sprintf("cold-%d", idx)
				}
			}
			u := fullPath + "?" + url.Values{*param: {k}}.Encode()
			method := http.MethodGet
			if m == modeRelease {
				method = http.MethodPost
			}
			req, _ := http.NewRequestWithContext(ctx, method, u, nil)
			resp, err := client.Do(req)
			if err == nil {
				// Drain and close body to enable connection reuse
				_, _ = io.Copy(io.Discard, resp.Body)
				_ = resp.Body.Close()
			} else {
				// Brief backoff on errors to avoid hot spinning
				time.Sleep(200 * time.Microsecond)
			}
		}
	}

	// Split N across conc workers
	per := *N / *conc
	rem := *N - per**conc
	var wg sync.WaitGroup
	wg.Add(*conc)
	for w := 0; w < *conc; w++ {
		count := per
		if w == *conc-1 {
			count += rem
		}
		go func(id, n int) {
			defer wg.Done()
			worker(id, n)
		}(w, count)
	}
	wg.Wait()
	elapsed := time.Since(start)
	if elapsed <= 0 {
		elapsed = time.Millisecond
	}
	ops := float64(*N) / elapsed.Seconds()
	fmt.Printf("LoadGen: mode=%s N=%d c=%d go=%d Duration=%s Throughput=%.0f req/s\n", m, *N, *conc, runtime.GOMAXPROCS(0), elapsed.Truncate(time.Millisecond), ops)
}
