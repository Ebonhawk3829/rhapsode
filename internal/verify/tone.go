// The tone runner: how rhapsode invokes the tone binary.
//
// rhapsode ships on the sandreas/m4b-tool image, where tone lives at
// /usr/local/bin/tone. The runner is a thin exec wrapper — every policy
// decision (which files, what to compare) belongs to the verifier, not to
// the process plumbing.
package verify

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// toneTimeout bounds one tone dump. A hung tone must not park the runner's
// sweep: the job stays in verifying and the next sweep retries.
const toneTimeout = 2 * time.Minute

// ToneDumper returns the dump function production uses: one `tone dump
// <path> --format json` per call, stdout captured, stderr discarded (tone
// writes progress chatter there; the JSON document is the contract).
func ToneDumper(ctx context.Context) func(string) (string, error) {
	return func(path string) (string, error) {
		runCtx, cancel := context.WithTimeout(ctx, toneTimeout)
		defer cancel()

		out, err := exec.CommandContext(runCtx, "tone", "dump", path, "--format", "json").Output()
		if err != nil {
			return "", fmt.Errorf("tone dump: %w", err)
		}
		return string(out), nil
	}
}

// trimNoise strips the non-JSON chatter some tone versions emit on stderr
// leakage into stdout, keeping only from the first '{'.
func trimNoise(raw string) string {
	if i := strings.Index(raw, "{"); i > 0 {
		return raw[i:]
	}
	return raw
}
