package checker_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/vtheofil/uptime-watch/internal/checker"
)

// Each case starts a real HTTP server on 127.0.0.1 that answers in a given way,
// then checks it. No internet and no mocks: the checker talks real HTTP.
func TestCheckStatusRule(t *testing.T) {
	tests := []struct {
		name       string
		handler    http.HandlerFunc
		wantOK     bool
		wantStatus int
	}{
		{
			name:       "200 is up",
			handler:    func(w http.ResponseWriter, r *http.Request) {},
			wantOK:     true,
			wantStatus: http.StatusOK,
		},
		{
			name: "redirect that ends in 200 is up",
			handler: func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/" {
					http.Redirect(w, r, "/final", http.StatusMovedPermanently)
				}
			},
			wantOK:     true,
			wantStatus: http.StatusOK,
		},
		{
			name: "404 is down",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusNotFound)
			},
			wantOK:     false,
			wantStatus: http.StatusNotFound,
		},
		{
			name: "500 is down",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusInternalServerError)
			},
			wantOK:     false,
			wantStatus: http.StatusInternalServerError,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(tt.handler)
			defer srv.Close()

			got := checker.Check(context.Background(), srv.URL, time.Second)

			if got.Err != nil {
				t.Fatalf("unexpected error: %v", got.Err)
			}
			if got.OK != tt.wantOK || got.StatusCode != tt.wantStatus {
				t.Errorf("got ok=%v status=%d, want ok=%v status=%d",
					got.OK, got.StatusCode, tt.wantOK, tt.wantStatus)
			}
		})
	}
}

func TestCheckSendsUserAgent(t *testing.T) {
	var seen string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.UserAgent()
	}))
	defer srv.Close()

	checker.Check(context.Background(), srv.URL, time.Second)

	if seen != "uptime-watch/0.1" {
		t.Errorf("User-Agent = %q, want %q", seen, "uptime-watch/0.1")
	}
}

func TestCheckTimeout(t *testing.T) {
	// A server that never answers, until the client gives up and disconnects.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer srv.Close()

	got := checker.Check(context.Background(), srv.URL, 100*time.Millisecond)

	if got.OK {
		t.Fatal("a server that never answers must be down")
	}
	if !errors.Is(got.Err, context.DeadlineExceeded) {
		t.Errorf("err = %v, want context.DeadlineExceeded", got.Err)
	}
	if got.Latency > time.Second {
		t.Errorf("latency = %v, the check did not stop at the timeout", got.Latency)
	}
}

func TestCheckRedirectLoopIsDown(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/", http.StatusFound)
	}))
	defer srv.Close()

	got := checker.Check(context.Background(), srv.URL, time.Second)

	if got.OK || got.Err == nil {
		t.Errorf("got ok=%v err=%v, want down with an error after 10 redirects", got.OK, got.Err)
	}
}

func TestCheckConnectionRefused(t *testing.T) {
	// Start a server only to learn a free address, then close it,
	// so nothing listens there any more.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	url := srv.URL
	srv.Close()

	got := checker.Check(context.Background(), url, time.Second)

	if got.OK || got.Err == nil || got.StatusCode != 0 {
		t.Errorf("got ok=%v status=%d err=%v, want down with status 0 and an error",
			got.OK, got.StatusCode, got.Err)
	}
}

func TestCheckStopsWhenParentIsCancelled(t *testing.T) {
	// This is what SIGTERM will do in the worker (M5): cancel the parent
	// context and every running check stops at once.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()

	got := checker.Check(ctx, srv.URL, time.Minute)

	if !errors.Is(got.Err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", got.Err)
	}
}
