package abb

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// FlareSolverr talks to the shared FlareSolverr instance and satisfies
// flareTalker.
//
// The request shape mirrors the Audible provider's: POST /v1 with
// cmd=request.get. The two adapters stay separate on purpose — different
// packages, different failure contracts — but the wire format is one thing,
// pinned once in each package's tests.
type FlareSolverr struct {
	endpoint string
	hc       *http.Client
}

// NewFlareSolverr builds a talker over a FlareSolverr base URL,
// e.g. http://flaresolverr:8191.
func NewFlareSolverr(endpoint string) *FlareSolverr {
	return &FlareSolverr{endpoint: strings.TrimSuffix(endpoint, "/"), hc: &http.Client{Timeout: 90 * time.Second}}
}

// Get solves through FlareSolverr and returns the page HTML.
//
// A solver-side failure or non-2xx wrapped status is ErrUnavailable: ABB is
// unreachable for this request, which the caller treats as a dead source —
// the same ride-through as the Audible provider's 503.
func (f *FlareSolverr) Get(ctx context.Context, url string) (string, error) {
	body, err := json.Marshal(map[string]any{
		"cmd":        "request.get",
		"url":        url,
		"maxTimeout": 60000,
	})
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, f.endpoint+"/v1", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := f.hc.Do(req)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 200))
		return "", fmt.Errorf("%w: solver %d %s", ErrUnavailable, resp.StatusCode, strings.TrimSpace(string(b)))
	}
	var sol struct {
		Status   string `json:"status"`
		Message  string `json:"message"`
		Solution struct {
			Status   int    `json:"status"`
			Response string `json:"response"`
		} `json:"solution"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&sol); err != nil {
		return "", fmt.Errorf("%w: decode: %v", ErrUnavailable, err)
	}
	if sol.Status != "ok" {
		return "", fmt.Errorf("%w: %s", ErrUnavailable, sol.Message)
	}
	if sol.Solution.Status < 200 || sol.Solution.Status >= 300 {
		return "", fmt.Errorf("%w: site %d", ErrUnavailable, sol.Solution.Status)
	}
	return sol.Solution.Response, nil
}
