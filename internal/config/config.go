// Package config loads rhapsode's configuration file.
//
// One file holds the whole deployment: where files live, which catalogues to
// ask, which sources to search, and which downloaders to drive. They are
// together because a deployment is one thing — the paths, the catalogues and
// the clients belong to the same setup, and splitting them means several files
// to mount and several to get out of sync.
//
// Every field has a zero value that means "not configured", and Load fills in
// the defaults. That is what lets a minimal file work: the user writes what
// they care about and nothing else.
package config

import (
	"bytes"
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// File is the whole configuration file.
type File struct {
	Server    Server    `yaml:"server"`
	Paths     Paths     `yaml:"paths"`
	Library   Library   `yaml:"library"`
	Providers Providers `yaml:"providers"`
	Sources   Sources   `yaml:"sources"`
	Grabbers  Grabbers  `yaml:"grabbers"`
	Notify    Notify    `yaml:"notify"`
}

// Server is the HTTP listener.
type Server struct {
	// Listen is the address the UI and API bind to. There is no
	// authentication, so binding a wider interface exposes full control of
	// the tool.
	Listen string `yaml:"listen"`
	// DataDir is where the SQLite database lives.
	DataDir string `yaml:"data_dir"`
}

// Paths are the filesystem locations rhapsode reads and writes.
type Paths struct {
	// Downloads is where the downloader writes completed files, as rhapsode
	// sees it. Each job stages under a slug subdirectory here.
	Downloads string `yaml:"downloads"`
	// Staging is local scratch space for merging and tagging. It must be on
	// local block storage: tone targets local filesystems, not network ones.
	Staging string `yaml:"staging"`
	// Library is the finished library, as rhapsode sees it.
	Library string `yaml:"library"`
}

// Library is how rhapsode talks to Audiobookshelf.
type Library struct {
	// URL is the ABS base URL.
	URL string `yaml:"url"`
	// Token is an ABS API key. It is a secret and belongs in SOPS, never in
	// the repository.
	Token string `yaml:"token"`
	// LibraryID is the ABS library to check for already-owned titles and to
	// scan after shelving.
	LibraryID string `yaml:"library_id"`
}

// Providers are the metadata catalogues.
type Providers struct {
	// GraphicAudio is the self-hosted GA scraper.
	GraphicAudio GraphicAudio `yaml:"graphicaudio"`
	// Audnexus is the public Audible metadata API.
	Audnexus Audnexus `yaml:"audnexus"`
	// Audible is the Audible storefront search, scraped.
	Audible Audible `yaml:"audible"`
}

// GraphicAudio is the self-hosted GA scraper.
type GraphicAudio struct {
	// URL is the scraper's base URL.
	URL string `yaml:"url"`
}

// Audnexus is the public Audible metadata API.
type Audnexus struct {
	// URL is the API base URL.
	URL string `yaml:"url"`
	// Region selects the catalogue region.
	Region string `yaml:"region"`
}

// Audible is the Audible storefront search. It is the only title→ASIN
// discovery surface in the stack: Audnexus is lookup-only, so without this
// the omnisearch finds only what the GA scraper indexes.
type Audible struct {
	// Base is the storefront to search. Different regions order differently
	// and hold different catalogues; the enricher's region should match it.
	Base string `yaml:"base"`
	// FlareSolverr is the challenge-solving proxy the scrape is routed
	// through. Audible geo-redirects and throttles datacentre IPs; from
	// this deployment the proxy is not optional. Empty means fetch direct,
	// which only makes sense from a residential IP.
	FlareSolverr string `yaml:"flaresolverr"`
}

// Sources are the places copies are found.
type Sources struct {
	// AudioBookBay is the torrent indexer.
	AudioBookBay AudioBookBay `yaml:"audiobookbay"`
	// Slskd is the Soulseek client.
	Slskd Slskd `yaml:"slskd"`
}

// AudioBookBay is the torrent indexer.
type AudioBookBay struct {
	// URL is the site's base URL. Mirrors are rotated on failure, not per
	// request.
	URL string `yaml:"url"`
	// Mirrors are alternate base URLs to fall back to.
	Mirrors []string `yaml:"mirrors"`
	// FlareSolverr is the challenge-solving proxy, already running for
	// Prowlarr.
	FlareSolverr string `yaml:"flaresolverr"`
}

// Slskd is the Soulseek client, used both as a source and as a grabber.
type Slskd struct {
	// URL is the slskd API base URL.
	URL string `yaml:"url"`
	// APIKey authenticates to slskd. A secret; keep it in SOPS.
	APIKey string `yaml:"api_key"`
}

// Grabbers are the downloaders.
type Grabbers struct {
	// Transmission is the torrent client.
	Transmission Transmission `yaml:"transmission"`
}

// Transmission is the torrent client.
type Transmission struct {
	// URL is the RPC endpoint.
	URL string `yaml:"url"`
	// Username and Password authenticate to the RPC endpoint.
	Username string `yaml:"username"`
	Password string `yaml:"password"`
	// Dir is where rhapsode's torrents land, as Transmission sees the path.
	// Inside the shared downloads tree so no other consumer's scanner
	// mistakes them for its own.
	Dir string `yaml:"dir"`
	// LocalDir is the same host directory as rhapsode sees it. The two
	// containers may mount the same tree at different names; verification
	// reads the completed files through rhapsode's view, so the mapping
	// between the two is configuration, not guesswork.
	LocalDir string `yaml:"local_dir"`
}

// Notify is where failures are reported.
type Notify struct {
	// Ntfy is the push endpoint. Only failures and a single confirmation are
	// sent; progress lives in the UI.
	Ntfy Ntfy `yaml:"ntfy"`
}

// Ntfy is the push endpoint.
type Ntfy struct {
	// URL is the ntfy server base URL.
	URL string `yaml:"url"`
	// Topic is the topic to publish to.
	Topic string `yaml:"topic"`
	// Token authenticates to ntfy, when the topic is protected.
	Token string `yaml:"token"`
}

// Load reads the configuration file at path, applies defaults, and validates
// the result.
//
// Unknown keys are an error rather than a silent no-op: a misspelled key that
// is quietly ignored is a setting the user believes is in force and is not.
func Load(path string) (*File, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}

	var f File
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	if err := dec.Decode(&f); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}

	f.applyDefaults(hasTopLevelKey(raw, "providers"))
	if err := f.validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &f, nil
}

// hasTopLevelKey reports whether the raw YAML contains a top-level `key:`.
// The decoder cannot distinguish "block absent" from "block present with
// blank values" after the fact; this raw scan preserves that distinction so
// defaults apply to a missing block without refilling a deliberate blank.
func hasTopLevelKey(raw []byte, key string) bool {
	for _, line := range bytes.Split(raw, []byte("\n")) {
		trimmed := bytes.TrimRight(line, " \t\r")
		if len(trimmed) < len(key)+1 {
			continue
		}
		if trimmed[0] == '#' {
			continue
		}
		if string(trimmed[:len(key)]) == key && trimmed[len(key)] == ':' {
			// Top-level keys start at column 0; indented keys are nested.
			if line[0] != ' ' && line[0] != '\t' {
				return true
			}
		}
	}
	return false
}

// applyDefaults fills in the values that have a sensible default. Anything
// deployment-specific — ports, credentials, hostnames — is left empty so that
// validate can insist on it rather than guessing.
func (f *File) applyDefaults(providersPresent bool) {
	if f.Server.Listen == "" {
		f.Server.Listen = "127.0.0.1:8099"
	}
	if f.Server.DataDir == "" {
		f.Server.DataDir = "/data"
	}
	if f.Paths.Downloads == "" {
		f.Paths.Downloads = "/downloads"
	}
	if f.Paths.Staging == "" {
		f.Paths.Staging = "/staging"
	}
	if f.Paths.Library == "" {
		f.Paths.Library = "/library"
	}
	// Provider URLs default only when the whole providers block is absent
	// (a fresh minimal config). An explicitly blank URL means off — the
	// degraded banner and the wiring gates both key off blankness, so
	// defaults must not refill a deliberate blank.
	if !providersPresent {
		f.Providers.GraphicAudio.URL = "http://100.64.0.1:8092"
		f.Providers.Audnexus.URL = "https://api.audnex.us"
		// The storefront must match where the server's IP actually lands:
		// audible.com geo-redirects a datacentre IP to its regional storefront,
		// and following that redirect means searching the home page, not the
		// search results. This deployment lands on .com.au, so the default
		// is .com.au and the Audnexus region below agrees with it: ASINs
		// are per-region edition identifiers, and an AU ASIN looked up in
		// the US catalogue 404s.
		f.Providers.Audible.Base = "https://www.audible.com.au"
		f.Providers.Audnexus.Region = "au"
	}
	if f.Providers.Audnexus.Region == "" {
		f.Providers.Audnexus.Region = "au"
	}
}

// validate rejects a configuration that cannot work.
//
// It checks only what would fail at runtime anyway, and it checks it at load
// so the failure names the file rather than surfacing hours later as a
// mysterious empty result.
//
// Presence is the switch: a component with its connection details filled in
// is on; blank them (or comment them out) to turn it off. There are no
// enabled flags — disabling means removing the details, and a degraded
// deployment says so loudly in the UI rather than hiding behind a toggle.
func (f *File) validate() error {
	if f.Server.Listen == "" {
		return fmt.Errorf("server.listen is required")
	}
	if f.Server.DataDir == "" {
		return fmt.Errorf("server.data_dir is required")
	}
	if f.Paths.Downloads == "" || f.Paths.Staging == "" || f.Paths.Library == "" {
		return fmt.Errorf("paths.downloads, paths.staging and paths.library are required")
	}
	if f.Paths.Staging == f.Paths.Library {
		return fmt.Errorf("paths.staging and paths.library must differ: staging is scratch, library is permanent")
	}
	if f.Library.URL == "" {
		return fmt.Errorf("library.url is required")
	}
	if f.Library.Token == "" {
		return fmt.Errorf("library.token is required")
	}
	if f.Library.LibraryID == "" {
		return fmt.Errorf("library.library_id is required")
	}
	// Optional components need no validation beyond their wiring gates in
	// main: a blank URL means off, and half-filled credentials fail loudly
	// at startup where the wiring checks them.
	return nil
}
