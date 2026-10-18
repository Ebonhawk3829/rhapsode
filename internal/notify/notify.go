// Package notify pushes the two moments the user cares about to ntfy: a
// finished book and a failure. Progress lives in the UI — a phone push per
// state transition would be noise, not signal.
package notify

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// postTimeout bounds one ntfy publish.
const postTimeout = 10 * time.Second

// Notifier publishes to one ntfy topic.
type Notifier struct {
	url   string
	topic string
	token string
	hc    *http.Client
}

// New builds a Notifier.
func New(url, topic, token string) *Notifier {
	return &Notifier{
		url:   strings.TrimRight(url, "/"),
		topic: topic,
		token: token,
		hc:    &http.Client{Timeout: postTimeout},
	}
}

// Send publishes one message. A notification failure is never a job failure:
// the push is a courtesy, and the audit trail carries the authoritative
// record. The error is returned for the caller to log.
func (n *Notifier) Send(ctx context.Context, title, message string) error {
	if n == nil || n.url == "" || n.topic == "" {
		return nil // notifications disabled — a silent no-op, not an error
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, n.url+"/"+n.topic, strings.NewReader(message))
	if err != nil {
		return err
	}
	req.Header.Set("Title", title)
	req.Header.Set("Tags", "books")
	if n.token != "" {
		req.Header.Set("Authorization", "Bearer "+n.token)
	}

	resp, err := n.hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 200))
		return fmt.Errorf("ntfy %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	return nil
}
