package systemd

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestValidateReadyCheck(t *testing.T) {
	for _, rc := range []ReadyCheck{
		{},
		{WaitPort: 8200},
		{HealthURL: "http://127.0.0.1:8200/health", Timeout: 5 * time.Second},
		{HealthURL: "https://localhost:8443/ready", WaitPort: 8443},
	} {
		if err := ValidateReadyCheck(rc); err != nil {
			t.Errorf("valid %+v rejected: %v", rc, err)
		}
	}
	for _, rc := range []ReadyCheck{
		{WaitPort: -1},
		{WaitPort: 65536},
		{HealthURL: "ftp://127.0.0.1/health"},
		{HealthURL: "http:///no-host"},
		{Timeout: -time.Second},
	} {
		if ValidateReadyCheck(rc) == nil {
			t.Errorf("invalid %+v accepted", rc)
		}
	}
}

func TestWaitReadyPortAndHealthURL(t *testing.T) {
	var dialHits, httpHits atomic.Int32
	m, _ := testManager(t)
	m.Dial = func(_ context.Context, network, addr string) (net.Conn, error) {
		if network != "tcp" || addr != "127.0.0.1:8200" {
			t.Fatalf("unexpected dial %s %s", network, addr)
		}
		if dialHits.Add(1) == 1 {
			return nil, errors.New("connection refused")
		}
		c1, c2 := net.Pipe()
		_ = c2.Close()
		return c1, nil
	}
	m.HTTPClient = &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			status := http.StatusOK
			if httpHits.Add(1) == 1 {
				status = http.StatusServiceUnavailable
			}
			return &http.Response{
				StatusCode: status,
				Body:       io.NopCloser(strings.NewReader("ok")),
			}, nil
		}),
	}

	err := m.waitReady(context.Background(), ReadyCheck{
		WaitPort:  8200,
		HealthURL: "http://127.0.0.1:8200/health",
		Timeout:   2 * time.Second,
	})
	if err != nil {
		t.Fatalf("waitReady failed: %v", err)
	}
	if dialHits.Load() < 2 || httpHits.Load() < 2 {
		t.Fatalf("expected retries: dialHits=%d httpHits=%d", dialHits.Load(), httpHits.Load())
	}
}

func TestWaitReadyTimeout(t *testing.T) {
	m, _ := testManager(t)
	m.Observe = func(ctx context.Context, _ time.Duration) error {
		return Wait(ctx, 10*time.Millisecond)
	}
	m.HTTPClient = &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusServiceUnavailable,
				Body:       io.NopCloser(strings.NewReader("not ready")),
			}, nil
		}),
	}
	err := m.waitReady(context.Background(), ReadyCheck{
		HealthURL: "http://127.0.0.1:8200/health",
		Timeout:   50 * time.Millisecond,
	})
	if err == nil || !strings.Contains(err.Error(), "就绪检查超时") {
		t.Fatalf("expected timeout error, got %v", err)
	}
}
