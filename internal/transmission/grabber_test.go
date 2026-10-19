package transmission

import (
	"context"
	"strings"
	"testing"

	"github.com/Ebonhawk3829/rhapsode/internal/grab"
	"github.com/Ebonhawk3829/rhapsode/internal/source"
)

func TestAddRejectsNonMagnet(t *testing.T) {
	g := New("http://localhost:9091/transmission/rpc", "/dl", "/local")
	if _, err := g.Add(context.Background(), source.Handle{Kind: source.Torrent, Ref: "not-a-magnet"}); err == nil {
		t.Error("Add accepted a non-magnet handle, want error")
	}
	if _, err := g.Add(context.Background(), source.Handle{Kind: source.Torrent}); err == nil {
		t.Error("Add accepted an empty handle, want error")
	}
}

func TestLocatedRequiresCapture(t *testing.T) {
	g := New("http://localhost:9091/transmission/rpc", "/dl", "/local")
	if _, err := g.Located(context.Background(), grab.ID("99")); err == nil {
		t.Error("Located without a prior Status capture succeeded, want error")
	}
}

func TestLocalPathPrefixSwap(t *testing.T) {
	g := New("http://example/rpc", "/media/downloads/kt-complete", "/downloads/kt-complete")
	got := g.localPath("/media/downloads/kt-complete/some-book/file.m4b", "ignored")
	_ = got
	// localPath joins downloadDir+file then swaps the prefix.
	got = g.localPath("/media/downloads/kt-complete/some-book", "file.m4b")
	if !strings.HasPrefix(got, "/downloads/kt-complete/") {
		t.Errorf("localPath = %q, want rhapsode-side prefix", got)
	}
	if !strings.HasSuffix(got, "file.m4b") {
		t.Errorf("localPath = %q, want the member filename", got)
	}
}

func TestPhaseFor(t *testing.T) {
	cases := []struct {
		status int
		done   float64
		want   grab.Phase
	}{
		{4, 1, grab.Done},
		{4, 0.5, grab.Active},
		{6, 1, grab.Done},
		{0, 0.2, grab.Stalled},
		{3, 0, grab.Queued},
		{1, 0, grab.Active},
		{99, 0, grab.Unknown},
	}
	for _, c := range cases {
		got := phaseFor(torrentRow{Status: c.status, PercentDone: c.done})
		if got != c.want {
			t.Errorf("phaseFor(status=%d, done=%v) = %v, want %v", c.status, c.done, got, c.want)
		}
	}
}
