<div align="center">

<img src="img/rhapsode-icon-dark-512.png" width="110" alt="rhapsode logo">

# rhapsode

An example of how an audiobook request pipeline can be built: one search across catalogues, ranked copies, verified downloads, reviewed tags, shelved books.

[![Release](https://img.shields.io/github/v/release/Ebonhawk3829/rhapsode?style=flat-square)](https://github.com/Ebonhawk3829/rhapsode/releases)
[![Docker Image](https://img.shields.io/badge/ghcr.io-ebonhawk3829%2Frhapsode-blue?style=flat-square&logo=docker)](https://github.com/Ebonhawk3829/rhapsode/pkgs/container/rhapsode)
[![License](https://img.shields.io/badge/license-MIT-green?style=flat-square)](LICENSE)
[![CI](https://img.shields.io/github/actions/workflow/status/Ebonhawk3829/rhapsode/ci.yml?label=CI&style=flat-square)](https://github.com/Ebonhawk3829/rhapsode/actions)

</div>

---

This is a worked example, not a turnkey app. It shows one way to stitch
together catalogue search, source ranking, download tracking, tag verification,
and library shelving into a coherent tool — take the patterns, adapt the
pieces to your own stack.

| Stage | Idea |
|---|---|
| **Search** | One omnisearch across metadata catalogues by author, series, title, ISBN, or ASIN |
| **Confirm** | Pick the edition, then pick the copy from offers ranked by match evidence |
| **Grab** | Hand the selection to a downloader and track it to completion |
| **Verify** | Check the staged files' own tags against the release before trusting them |
| **Merge** | Combine parts into one file without re-encoding |
| **Review** | Propose tags from the catalogue record; require human edit-and-confirm |
| **Shelve** | Copy into a deterministic library layout and trigger a rescan |
| **Clean** | Remove scratch on every terminal path, plus a startup sweep for crash debris |

Adaptation points: the catalogue providers, the sources, the downloaders, the
tagging tools, and the library layout are all interfaces. The interesting
decisions — scoring within a source instead of across them, verifying embedded
tags instead of trusting share text, blocking on tag review — transfer to any
stack.

## Contents

- [Status](#status)
- [Requirements](#requirements)
- [Installation](#installation)
- [Configuration](#configuration)
- [How matching works](#how-matching-works)
- [Job states](#job-states)
- [Flags](#flags)
- [API](#api)
- [Security](#security)
- [License](#license)

## Status

Built for one homelab, published as inspiration.

Releases follow semver. Patch releases are safe to take unread. Minor releases
may add configuration keys; existing ones keep working. A major release may
change behaviour, and the release notes will say so.

What it assumes:

- **One user.** No auth, no permissions model. See [Security](#security).
- **A specific stack.** slskd, Transmission, FlareSolverr, Audiobookshelf,
  m4b-tool, tone — each behind an interface, but the defaults name these tools.
- **Human review.** The tag step always parks for edit and confirm; scores
  advise, they never decide across sources.

## Requirements

The shape of the stack, not a shopping list:

- Download clients with remote APIs (here: slskd and Transmission).
- Metadata catalogues (here: a GraphicAudio scraper, Audible search, Audnexus).
- A challenge solver where the network needs one (here: FlareSolverr).
- A library with a scan API (here: Audiobookshelf).
- Audio tooling in the image (`m4b-tool` for merging, `tone` for tags).
- Go 1.27+ to build from source; Docker needs nothing else.
- Optionally, a push endpoint for alerts (here: ntfy).

## Installation

### Docker Compose

The example below is one deployment, not a universal template — adjust image
tag, paths, and user to your setup. The pattern that matters: one mount for
the downloads tree the downloaders write, local scratch for audio work, and
the library where your reader watches.

```yaml
services:
  rhapsode:
    image: ghcr.io/ebonhawk3829/rhapsode:latest
    user: "1000:1000"             # your media user's uid:gid
    volumes:
      - ./config:/data            # database and config file
      - ./downloads:/downloads    # shared downloads tree (same host dir the downloaders write)
      - ./staging:/staging        # local scratch for merging (local block storage, not a network mount)
      - ./library:/library        # finished library (your reader watches this too)
    ports:
      - "127.0.0.1:8099:8099"
    restart: unless-stopped
    command:
      - "-config"
      - "/data/rhapsode.yaml"
```

Copy `rhapsode.yaml.example` to your config dir and fill in service URLs and
keys. Blank details mean that piece is off, and the UI says so with a
degraded banner rather than failing silently.

Deployment notes worth stealing:

**Share one downloads tree.** Whatever the downloaders write must be readable
at the same relative paths — verification locates files through the
transport's own API, translated across mount views.

**Keep audio scratch local.** Tag tooling targets local filesystems, not
network mounts.

**Bind loopback and reverse-proxy.** There is no authentication; expose the
port through a tailnet or proxy, never directly.

## Configuration

Copy `rhapsode.yaml.example` to `rhapsode.yaml`. A minimal file names only
what it uses. Presence is the switch: a component with its connection details
filled in is on, blank them to turn it off. There are no `enabled` flags.

One subtlety worth knowing if you adapt the catalogue code: storefront
regions must agree end to end. A geo-redirected search base returns the wrong
page silently, and per-region identifiers looked up in the wrong catalogue
404 — both degrade to thin results rather than errors.

## How matching works

Two comparisons, two directions:

Pre-download, offers are graded on what the source shows — share text or
catalogue rows: title, author, narrator, series, part structure, format, and
whether audio files are actually present. Scores compare within a source, so
different evidence types never fuse into one number.

Post-download, the staged files' own embedded tags are checked against the
release: title containment (subtitles pass), per-voice narrator overlap
(co-narration passes), abridgement markers, soft misses for tags folder rips
omit. Contradictions park the job back for another pick; only matches proceed.

## Job states

`pending → searching → confirm → downloading → verifying → merging →
tag-review → tagging → shelving → done`, with `failed` and `cancelled` as
terminal exits. The UI renders friendly labels (`Pick a Copy`, `Review Tags`)
rather than the internal names. The jobs list hides shelved jobs; failed and
cancelled rows clear with a button.

## Flags

| Flag | Description |
|---|---|
| `-config` | Path to the configuration file (default `/data/rhapsode.yaml`, or `$RHAPSODE_CONFIG`) |
| `-version` | Print the version and exit |

## API

The handlers speak JSON alongside the HTML: `GET /api/search?q=`,
`POST /api/jobs`, `GET /api/jobs`, `GET /api/jobs/{id}`,
`GET /api/jobs/{id}/candidates`, `POST /api/jobs/{id}/confirm`,
`POST /api/jobs/{id}/reject`, `POST /api/jobs/{id}/tags`,
`POST /api/jobs/{id}/cancel`, `DELETE /api/jobs/{id}`, `GET /api/version`.

## Security

There is no authentication on the web UI or API. Anyone who can reach the
port has full control: search, download, tag, shelve, cancel, and clear.
Bind loopback and put a reverse proxy in front; never expose the port to a
network you do not trust.

Secrets (API keys, tokens) are `${VAR}` references expanded from the
environment at startup — pass them from your compose file or process
environment rather than writing them into the config. An unset variable
expands to empty, which disables that piece with a notice instead of failing
to start. Never commit secrets to the repository.

## License

MIT. See [LICENSE](LICENSE).
