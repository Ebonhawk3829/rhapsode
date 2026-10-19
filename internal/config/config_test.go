package config

import (
	"os"
	"path/filepath"
	"testing"
)

func writeCfg(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "rhapsode.yaml")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func minimal() string {
	return `
library:
  url: http://abs:13378
  token: secret
  library_id: lib1
`
}

func TestDefaultsApplied(t *testing.T) {
	f, err := Load(writeCfg(t, minimal()))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if f.Server.Listen != "127.0.0.1:8099" {
		t.Errorf("listen = %q", f.Server.Listen)
	}
	if f.Paths.Downloads != "/downloads" || f.Paths.Staging != "/staging" || f.Paths.Library != "/library" {
		t.Errorf("paths = %+v", f.Paths)
	}
}

func TestUnknownKeysRejected(t *testing.T) {
	if _, err := Load(writeCfg(t, minimal()+"\nbogus_key: 1\n")); err == nil {
		t.Error("unknown key accepted, want error")
	}
}

func TestMissingLibraryRejected(t *testing.T) {
	if _, err := Load(writeCfg(t, "server:\n  listen: 127.0.0.1:8099\n")); err == nil {
		t.Error("config without library accepted, want error")
	}
}

func TestStagingEqualsLibraryRejected(t *testing.T) {
	body := minimal() + "\npaths:\n  downloads: /d\n  staging: /same\n  library: /same\n"
	if _, err := Load(writeCfg(t, body)); err == nil {
		t.Error("staging == library accepted, want error")
	}
}

func TestPresenceGating(t *testing.T) {
	// A blank URL means off; a filled one means on. There are no enabled
	// flags — disabling is blanking the details.
	f, err := Load(writeCfg(t, minimal()))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if f.Providers.GraphicAudio.URL == "" {
		t.Error("default GA URL not applied")
	}
	if f.Sources.Slskd.URL != "" || f.Sources.Slskd.APIKey != "" {
		t.Errorf("slskd should default to blank (off), got %+v", f.Sources.Slskd)
	}
}

func TestExplicitBlankProviderStaysOff(t *testing.T) {	body := minimal() + "\nproviders:\n  graphicaudio:\n    url: \"\"\n  audnexus:\n    url: \"\"\n  audible:\n    base: \"\"\n"
	f, err := Load(writeCfg(t, body))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if f.Providers.GraphicAudio.URL != "" {
		t.Errorf("GA URL = %q, want blank (explicit off)", f.Providers.GraphicAudio.URL)
	}
	if f.Providers.Audnexus.URL != "" {
		t.Errorf("Audnexus URL = %q, want blank", f.Providers.Audnexus.URL)
	}
	if f.Providers.Audible.Base != "" {
		t.Errorf("Audible base = %q, want blank", f.Providers.Audible.Base)
	}
}

func TestEnvExpansion(t *testing.T) {
	t.Setenv("RHAPSODE_TEST_SECRET", "s3cret")
	f, err := Load(writeCfg(t, minimal()+`
sources:
  slskd:
    url: http://x
    api_key: ${RHAPSODE_TEST_SECRET}
`))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if f.Sources.Slskd.APIKey != "s3cret" {
		t.Errorf("api_key = %q, want expanded secret", f.Sources.Slskd.APIKey)
	}
}

func TestMissingEnvExpandsToEmpty(t *testing.T) {
	f, err := Load(writeCfg(t, minimal()+`
sources:
  slskd:
    url: http://x
    api_key: ${RHAPSODE_DEFINITELY_UNSET_VAR}
`))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if f.Sources.Slskd.APIKey != "" {
		t.Errorf("api_key = %q, want empty (unset var disables the piece)", f.Sources.Slskd.APIKey)
	}
}
