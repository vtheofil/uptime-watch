package checker

import (
	"context"
	"net/http"
	"time"
)

type Result struct {
	OK         bool
	StatusCode int
	Latency    time.Duration
	Err        error
}

func Check(ctx context.Context, url string, timeout time.Duration) Result {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return Result{Err: err}
	}
	req.Header.Set("User-Agent", "uptime-watch/0.1")

	start := time.Now()
	resp, err := http.DefaultClient.Do(req)
	latency := time.Since(start)

	if err != nil {
		return Result{Latency: latency, Err: err}
	}
	defer resp.Body.Close()

	return Result{
		OK:         resp.StatusCode < 400,
		StatusCode: resp.StatusCode,
		Latency:    latency,
	}
}
