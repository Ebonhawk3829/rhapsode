// Package version reports what build this binary is.
package version

import "runtime"

// Version is injected at build time via -ldflags. "dev" means an uninstrumented
// local build.
var Version = "dev"

// String returns the version.
func String() string { return Version }

// GoVersion returns the Go toolchain that built the binary.
func GoVersion() string { return runtime.Version() }
