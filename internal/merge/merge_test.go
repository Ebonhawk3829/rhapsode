package merge

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func indexOf(args []string, want string) int {
	for i, a := range args {
		if a == want {
			return i
		}
	}
	return -1
}

func newTestMerger(t *testing.T, run func(ctx context.Context, args []string) error) (*Merger, string) {
	t.Helper()
	staging := t.TempDir()
	m := New(staging)
	if run != nil {
		m.SetRunner(run)
	}
	return m, staging
}

func TestMergeSingleFilePassesThrough(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "staged.m4b")
	if err := os.WriteFile(src, []byte("audio-bytes"), 0o644); err != nil {
		t.Fatal(err)
	}

	var called bool
	m, staging := newTestMerger(t, func(context.Context, []string) error {
		called = true
		return nil
	})

	res, err := m.Merge(context.Background(), "The Black Prism", []string{src})
	if err != nil {
		t.Fatalf("Merge: %v", err)
	}
	if called {
		t.Error("m4b-tool ran for a single-file batch, want pass-through")
	}
	if res.Merged {
		t.Error("Result.Merged = true for a pass-through")
	}
	if res.Path != filepath.Join(staging, "the-black-prism.m4b") {
		t.Errorf("path = %q", res.Path)
	}
	got, err := os.ReadFile(res.Path)
	if err != nil {
		t.Fatalf("read output: %v", err)
	}
	if string(got) != "audio-bytes" {
		t.Error("pass-through did not copy the bytes")
	}
}

func TestMergeMultiFileInvokesM4bTool(t *testing.T) {
	var gotArgs []string
	m, staging := newTestMerger(t, func(_ context.Context, args []string) error {
		gotArgs = args
		// The runner "produces" the output file, like the real tool does.
		out := args[indexOf(args, "--output-file")+1]
		return os.WriteFile(out, []byte("merged"), 0o644)
	})

	inputs := []string{"/staged/part1.m4b", "/staged/part2.m4b", "/staged/part3.m4b"}
	res, err := m.Merge(context.Background(), "The Black Prism", inputs)
	if err != nil {
		t.Fatalf("Merge: %v", err)
	}
	if !res.Merged {
		t.Error("Result.Merged = false for a multi-file merge")
	}
	if res.Path != filepath.Join(staging, "the-black-prism.m4b") {
		t.Errorf("path = %q", res.Path)
	}
	// The contract: every input, the output, no re-encoding, no cache.
	joined := strings.Join(gotArgs, " ")
	for _, want := range inputs {
		if !strings.Contains(joined, want) {
			t.Errorf("input %q missing from args: %v", want, gotArgs)
		}
	}
	if !strings.Contains(joined, "--no-conversion") {
		t.Errorf("merge must not re-encode: %v", gotArgs)
	}
	if !strings.Contains(joined, "--no-cache") {
		t.Errorf("merge must not reuse stale cache: %v", gotArgs)
	}
}

func TestMergeToolFailureSurfacesTail(t *testing.T) {
	m, _ := newTestMerger(t, func(context.Context, []string) error {
		return errors.New("exit status 1: chapter detection failed")
	})
	_, err := m.Merge(context.Background(), "Book", []string{"/a.m4b", "/b.m4b"})
	if err == nil {
		t.Fatal("a tool failure vanished")
	}
	if !strings.Contains(err.Error(), "chapter detection failed") {
		t.Errorf("error lost the tool's message: %v", err)
	}
}

func TestMergeOutputMustExist(t *testing.T) {
	// A tool that exits 0 without producing the file is a failure, not a
	// success — the tag driver would otherwise tag a ghost.
	m, _ := newTestMerger(t, func(context.Context, []string) error { return nil })
	if _, err := m.Merge(context.Background(), "Book", []string{"/a.m4b", "/b.m4b"}); err == nil {
		t.Fatal("missing output accepted")
	}
}

func TestMergeRejectsEmptyTitleAndInputs(t *testing.T) {
	m, _ := newTestMerger(t, nil)
	if _, err := m.Merge(context.Background(), "", []string{"/a.m4b"}); err == nil {
		t.Error("empty title accepted")
	}
	if _, err := m.Merge(context.Background(), "Book", nil); err == nil {
		t.Error("empty inputs accepted")
	}
}

func TestSlugify(t *testing.T) {
	cases := map[string]string{
		"The Black Prism":  "the-black-prism",
		"Book: A Subtitle": "book-a-subtitle",
		"  Spaces  ":       "spaces",
		"":                 "",
	}
	for in, want := range cases {
		if got := slugify(in); got != want {
			t.Errorf("slugify(%q) = %q, want %q", in, got, want)
		}
	}
}
