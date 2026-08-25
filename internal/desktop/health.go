package desktop

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const maxHealthResponseBytes = 1024

type Health struct {
	Status string `json:"status"`
}

func CheckHealth(ctx context.Context, host string, port int, timeout time.Duration) (Health, error) {
	if ctx == nil {
		return Health{}, errors.New("health context is required")
	}
	ip := net.ParseIP(host)
	if !strings.EqualFold(host, "localhost") && (ip == nil || !ip.IsLoopback()) {
		return Health{}, fmt.Errorf("health endpoint must be loopback, got %q", host)
	}
	if port < 1 || port > 65535 {
		return Health{}, fmt.Errorf("health port must be between 1 and 65535, got %d", port)
	}
	if timeout <= 0 {
		timeout = 3 * time.Second
	}
	client := &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			Proxy:             nil,
			DialContext:       (&net.Dialer{Timeout: timeout}).DialContext,
			DisableKeepAlives: true,
		},
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+net.JoinHostPort(host, strconv.Itoa(port))+"/healthz", nil)
	if err != nil {
		return Health{}, fmt.Errorf("build health request: %w", err)
	}
	response, err := client.Do(request)
	if err != nil {
		return Health{}, fmt.Errorf("request node health: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, maxHealthResponseBytes))
		return Health{}, fmt.Errorf("node health returned HTTP %d", response.StatusCode)
	}
	var health Health
	decoder := json.NewDecoder(io.LimitReader(response.Body, maxHealthResponseBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&health); err != nil {
		return Health{}, fmt.Errorf("decode node health: %w", err)
	}
	if health.Status != "ok" {
		return Health{}, fmt.Errorf("node health status is %q", health.Status)
	}
	return health, nil
}
