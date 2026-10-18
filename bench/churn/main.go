// Command churn opens a connection, runs one query and closes it, in a loop.
// It simulates serverless clients that cannot keep connections open.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
)

func main() {
	dsn := flag.String("dsn", "postgres://app:app@localhost:6432/appdb?sslmode=disable", "connection string")
	workers := flag.Int("workers", 50, "concurrent workers")
	duration := flag.Duration("duration", 30*time.Second, "test duration")
	query := flag.String("query", "SELECT 1", "query to run on each connection")
	csvOut := flag.Bool("csv", false, "print one CSV line: workers,conns_per_sec,p50_ms,p95_ms,p99_ms,errors")
	flag.Parse()

	cfg, err := pgx.ParseConfig(*dsn)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	ctx, cancel := context.WithTimeout(context.Background(), *duration)
	defer cancel()

	var (
		mu      sync.Mutex
		lat     []time.Duration
		errs    atomic.Int64
		firstEr atomic.Value
		wg      sync.WaitGroup
	)
	start := time.Now()
	for w := 0; w < *workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var local []time.Duration
			for ctx.Err() == nil {
				t0 := time.Now()
				if err := once(ctx, cfg, *query); err != nil {
					if ctx.Err() != nil {
						break
					}
					errs.Add(1)
					firstEr.CompareAndSwap(nil, err.Error())
					continue
				}
				local = append(local, time.Since(t0))
			}
			mu.Lock()
			lat = append(lat, local...)
			mu.Unlock()
		}()
	}
	wg.Wait()
	elapsed := time.Since(start)

	slices.Sort(lat)
	pct := func(q float64) float64 {
		if len(lat) == 0 {
			return 0
		}
		return float64(lat[min(len(lat)-1, int(q*float64(len(lat))))].Microseconds()) / 1000
	}
	rate := float64(len(lat)) / elapsed.Seconds()
	if *csvOut {
		fmt.Printf("%d,%.1f,%.2f,%.2f,%.2f,%d\n", *workers, rate, pct(.5), pct(.95), pct(.99), errs.Load())
		return
	}
	fmt.Printf("workers=%d connections=%d rate=%.1f/s p50=%.2fms p95=%.2fms p99=%.2fms errors=%d\n",
		*workers, len(lat), rate, pct(.5), pct(.95), pct(.99), errs.Load())
	if e := firstEr.Load(); e != nil {
		fmt.Printf("first error: %s\n", e)
	}
}

