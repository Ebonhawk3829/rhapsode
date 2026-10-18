package notify

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSendPublishesTitleAndMessage(t *testing.T) {
	var (
		gotTopic string
		gotTitle string
		gotBody  string
		gotAuth  string
		gotTags  string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotTopic = strings.TrimPrefix(r.URL.Path, "/")
		gotTitle = r.Header.Get("Title")
		gotTags = r.Header.Get("Tags")
		gotAuth = r.Header.Get("Authorization")
		b := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(b)
		gotBody = string(b)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	n := New(srv.URL, "rhapsode", "secret")
	if err := n.Send(context.Background(), "Book shelved", "The Black Prism is in the library"); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if gotTopic != "rhapsode" {
		t.Errorf("topic = %q", gotTopic)
	}
	if gotTitle != "Book shelved" {
		t.Errorf("title = %q", gotTitle)
	}
	if gotBody != "The Black Prism is in the library" {
		t.Errorf("body = %q", gotBody)
	}
	if gotTags != "books" {
		t.Errorf("tags = %q", gotTags)
	}
	if gotAuth != "Bearer secret" {
		t.Errorf("auth = %q", gotAuth)
	}
}

func TestSendFailureReturnsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()

	n := New(srv.URL, "topic", "")
	if err := n.Send(context.Background(), "t", "m"); err == nil {
		t.Fatal("a failed publish vanished")
	}
}

func TestDisabledNotifierIsSilent(t *testing.T) {
	// A nil notifier and an unconfigured one are no-ops, not errors: the
	// notification is a courtesy and an unconfigured deployment must run.
	var nilNotifier *Notifier
	if err := nilNotifier.Send(context.Background(), "t", "m"); err != nil {
		t.Errorf("nil notifier errored: %v", err)
	}
	if err := New("", "", "").Send(context.Background(), "t", "m"); err != nil {
		t.Errorf("unconfigured notifier errored: %v", err)
	}
}
