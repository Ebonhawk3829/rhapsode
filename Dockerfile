# rhapsode — audiobook request platform.
#
# Multi-stage: build the static Go binary, then ship it on the m4b-tool image
# so the merge and tag tooling lives in the same image as the app that drives
# it. m4b-tool already carries tone, ffmpeg, mp4v2 and fdkaac, so the runtime
# stage adds only the binary and a healthcheck client.

# Pin the base by digest: every merge/tag assumption (m4b-tool flags, tone
# atoms, ffmpeg behaviour) depends on this image's tooling. The repo
# publishes only `latest`, so the digest is the pin — refresh it
# deliberately, not on every build.
# (Declared before the first FROM so both stages can use it.)
ARG M4B_TOOL_DIGEST=sha256:3154399019ed6a2d26fd477f71a1367e4c60e605d0188699028ad3444afd3c70

FROM golang:1.27 AS build

WORKDIR /src

# Dependencies first, so a code change does not re-download them.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# Version is injected so the binary can report what build it is. Without it a
# pinned image tag is the only clue, and a tag can be re-pushed.
ARG VERSION=dev

# CGO off gives a static binary that runs on any base image.
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath \
    -ldflags "-X github.com/Ebonhawk3829/rhapsode/internal/version.Version=${VERSION}" \
    -o /out/rhapsode ./cmd/rhapsode

FROM sandreas/m4b-tool@${M4B_TOOL_DIGEST}

# curl is not in the base image (only the php83-curl extension), and the
# healthcheck needs a client. ca-certificates is added for the outbound HTTPS
# to Audnexus. 7zip extracts torrent-wrapped archives before verify runs.
RUN apk add --no-cache curl ca-certificates 7zip

COPY --from=build /out/rhapsode /usr/local/bin/rhapsode

# Data lives here: the SQLite database and the configuration file. Mount a
# volume at /data to persist them.
RUN mkdir -p /data && chown 1001:1001 /data
WORKDIR /data

# Run unprivileged, matching the media user's uid/gid so files land with
# ownership the other containers accept.
USER 1001:1001

# 8099 is the web UI and API.
EXPOSE 8099

ENV RHAPSODE_CONFIG=/data/rhapsode.yaml

# /healthz is deliberately shallow — it does not touch the database or the
# network, so a slow upstream cannot mark a correctly-running container
# unhealthy. The port must match server.listen in rhapsode.yaml.
HEALTHCHECK --interval=60s --timeout=5s --start-period=10s --retries=3 \
    CMD curl -fsS http://127.0.0.1:8099/healthz || exit 1

# The base image's entrypoint is m4b-tool; rhapsode drives it as a subprocess,
# so the entrypoint is replaced rather than chained.
ENTRYPOINT ["/usr/local/bin/rhapsode"]
CMD ["-config", "/data/rhapsode.yaml"]
