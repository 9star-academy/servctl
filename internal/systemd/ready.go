package systemd

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

const defaultReadyTimeout = 15 * time.Second

type ReadyCheck struct {
	HealthURL string
	WaitPort  int
	Timeout   time.Duration
}

func (r ReadyCheck) Enabled() bool {
	return r.WaitPort > 0 || r.HealthURL != ""
}

func ValidateReadyCheck(r ReadyCheck) error {
	if r.WaitPort < 0 || r.WaitPort > 65535 {
		return fmt.Errorf("无效 --wait-port %d：端口必须在 1-65535 之间", r.WaitPort)
	}
	if r.HealthURL != "" {
		u, err := url.Parse(r.HealthURL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return fmt.Errorf("无效 --health-url %q：必须为有效的 http:// 或 https:// 地址", r.HealthURL)
		}
	}
	if r.Timeout < 0 {
		return fmt.Errorf("--ready-timeout 不能为负数")
	}
	return nil
}

func (m *Manager) waitReady(ctx context.Context, r ReadyCheck) error {
	if !r.Enabled() {
		return nil
	}
	if err := ValidateReadyCheck(r); err != nil {
		return err
	}
	timeout := r.Timeout
	if timeout == 0 {
		timeout = defaultReadyTimeout
	}
	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	client := m.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 2 * time.Second}
	}
	dial := m.Dial
	if dial == nil {
		var dialer net.Dialer
		dial = dialer.DialContext
	}
	var lastErr error
	const pollInterval = 250 * time.Millisecond

	for {
		if r.WaitPort > 0 {
			lastErr = checkTCPPort(waitCtx, dial, r.WaitPort)
		} else {
			lastErr = nil
		}
		if lastErr == nil && r.HealthURL != "" {
			lastErr = checkHealthURL(waitCtx, client, r.HealthURL)
		}
		if lastErr == nil {
			return nil
		}
		if waitCtx.Err() != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return fmt.Errorf("就绪检查超时（%s）: %w", timeout, lastErr)
		}
		if err := m.Observe(waitCtx, pollInterval); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return fmt.Errorf("就绪检查超时（%s）: %w", timeout, lastErr)
		}
	}
}

func checkTCPPort(ctx context.Context, dial func(context.Context, string, string) (net.Conn, error), port int) error {
	dialCtx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	conn, err := dial(dialCtx, "tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		return fmt.Errorf("端口 %d 尚未就绪: %w", port, err)
	}
	_ = conn.Close()
	return nil
}

func checkHealthURL(ctx context.Context, client *http.Client, rawURL string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("健康检查请求失败 %s: %w", rawURL, err)
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	_ = resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("健康检查返回非 2xx 状态码: HTTP %d", resp.StatusCode)
	}
	return nil
}
