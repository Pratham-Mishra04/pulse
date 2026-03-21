package engine

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/Pratham-Mishra04/pulse/internal/log"
)

// HealthPoller polls an HTTP endpoint until the process is healthy.
// "Healthy" means receiving cfg.Threshold consecutive HTTP 200 responses.
type HealthPoller struct {
	port   int
	cfg    HealthCheckConfig
	client *http.Client
	log    *log.Logger
}

// NewHealthPoller creates a HealthPoller for the process listening on port.
func NewHealthPoller(port int, cfg HealthCheckConfig, l *log.Logger) *HealthPoller {
	// Cap per-request timeout to the lesser of the total health check budget
	// and 5s. Using cfg.Interval here would be wrong — a short poll cadence
	// (e.g. 100ms) would abort every probe before the endpoint has a chance
	// to respond, making promotion impossible.
	reqTimeout := min(cfg.Timeout, 5*time.Second)
	return &HealthPoller{
		port: port,
		cfg:  cfg,
		log:  l,
		client: &http.Client{
			Timeout: reqTimeout,
		},
	}
}

// Poll blocks until the process is healthy, ctx is cancelled, or the timeout
// budget (cfg.Timeout) is exhausted.
//
// Returns nil when cfg.Threshold consecutive 200s are observed.
// Returns an error on timeout or cancellation.
func (h *HealthPoller) Poll(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, h.cfg.Timeout)
	defer cancel()

	consecutive := 0
	ticker := time.NewTicker(h.cfg.Interval)
	defer ticker.Stop()

	// Probe immediately before waiting for the first tick.
	if h.probe(ctx) {
		consecutive++
		if consecutive >= h.cfg.Threshold {
			return nil
		}
	}

	for {
		select {
		case <-ctx.Done():
			if ctx.Err() == context.DeadlineExceeded {
				return fmt.Errorf("health check timed out after %s", h.cfg.Timeout)
			}
			return fmt.Errorf("health check cancelled: %w", ctx.Err())
		case <-ticker.C:
			if h.probe(ctx) {
				consecutive++
				if consecutive >= h.cfg.Threshold {
					return nil
				}
			} else {
				consecutive = 0
			}
		}
	}
}

// probe performs a single GET request and returns true if the response is 200.
func (h *HealthPoller) probe(ctx context.Context) bool {
	url := fmt.Sprintf("http://127.0.0.1:%d%s", h.port, h.cfg.Path)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return false
	}
	resp, err := h.client.Do(req)
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}
