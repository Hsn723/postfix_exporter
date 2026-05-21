# CLAUDE.md

Guidance for Claude Code sessions working in this repository.

## Overview

`postfix_exporter` is a Prometheus exporter for the Postfix mail server, written in Go. It exposes:

- Histogram metrics for the size and age of messages in the Postfix mail queue, collected by connecting to Postfix's `showq` (typically a UNIX socket under `/var/spool/postfix/public/showq`, optionally a TCP socket).
- Counters and other metrics derived from parsing Postfix log entries via regular expressions. Log entries can be sourced from a log file, the systemd journal, Docker container logs, or Kubernetes pod logs.

Metrics are served over HTTP on port `9154` by default at `/metrics`.

This is an upstream fork (origin `Hsn723/postfix_exporter`) mirrored under Paubox. The Paubox copy currently tracks upstream `master`; downstream changes should be kept minimal and clearly identified.

## Layout

Top-level Go module: `github.com/hsn723/postfix_exporter` (Go 1.24).

- `main.go` — entry point. Defines kingpin CLI flags, wires log sources to exporters, configures the Prometheus HTTP server (via `prometheus/exporter-toolkit`), and optionally runs a watchdog loop that recycles the exporter when a log source becomes unhealthy.
- `exporter/` — the core `PostfixExporter` Prometheus collector. Parses Postfix log lines and translates them into metrics. Service-label options (`WithCleanupLabels`, `WithSmtpLabels`, etc.) handle Postfix's user-defined service labels from `master.cf`.
- `showq/` — client for Postfix's `showq` service. Reads queue contents over either a UNIX socket or TCP and emits queue size/age histograms.
- `logsource/` — pluggable log-source factories selected at runtime:
  - `logsource.go` — `LogSource`, `LogSourceCloser`, and `LogSourceFactory` interfaces plus factory registration.
  - `logsource_file.go` — tails a log file (`--postfix.logfile_path`), using `nxadm/tail`. Default source.
  - `logsource_systemd.go` — reads the systemd journal (`--systemd.enable`). Requires CGO + `libsystemd-dev`; excluded by the `nosystemd` build tag.
  - `logsource_docker.go` — streams Docker container logs (`--docker.enable`). Excluded by the `nodocker` build tag.
  - `logsource_kubernetes.go` — follows pod logs via `client-go` (`--kubernetes.enable`); requires showq over TCP. Excluded by the `nokubernetes` build tag.
- `charts/postfix-exporter/` — Helm chart published to `https://hsn723.github.io/postfix_exporter`.
- `Dockerfile` — `FROM scratch`, expects a prebuilt `postfix_exporter` binary copied in. The published image is built without docker/systemd/kubernetes support.
- `Makefile` — top-level build, test, lint, and container-test targets.
- `VERSION` — embedded into the binary via `//go:embed` as a fallback when `-ldflags -X main.version=...` is not set.
- `cst.yaml`, `ct.yaml` — container-structure-test and chart-testing configs used in CI.

## Build, run, test

Default build excludes docker, systemd, and kubernetes support:

```sh
go build -tags nosystemd,nodocker,nokubernetes .
```

Selectively enable backends by dropping the corresponding tag. Building with `systemd` requires `libsystemd-dev` on Debian/Ubuntu (the `Makefile`'s `libsystemd-dev` target installs it).

Make targets:

- `make build` — `go build -ldflags "-s -w" .` (installs `libsystemd-dev` first; assumes Debian/Ubuntu).
- `make test` — `go test -coverprofile cover.out -count=1 -race -p 4 -v ./...`. Also depends on `libsystemd-dev`.
- `make lint` — installs `pre-commit` if missing and runs all configured hooks.
- `make container-structure-test` — runs `container-structure-test` against the prerelease container image for each goarch defined in `.goreleaser.yml`.

Run a built binary, tailing a log file:

```sh
./postfix_exporter \
  --postfix.showq_path=/var/spool/postfix/public/showq \
  --postfix.logfile_path=/var/log/mail.log
```

Switch log source with `--systemd.enable`, `--docker.enable`, or `--kubernetes.enable` (mutually exclusive; the chosen backend overrides the file source). See `README.md` for the full flag table.

When `showq` is reached over TCP (Kubernetes or remote setups), set `--postfix.showq_network=tcp` and `--postfix.showq_port`.

## Configuration flags worth knowing

- `--web.listen-address` (default `:9154`), `--web.telemetry-path` (default `/metrics`), `--web.config.file` for TLS / basic auth via `exporter-toolkit`.
- `--postfix.${SERVICE_TYPE}_service_label` (repeatable) maps user-defined Postfix service labels from `master.cf` to their underlying service type so log parsing finds the right counters.
- `--log.unsupported` logs any Postfix log line the parser does not recognize; useful when adding new patterns.
- `--watchdog` enables a 5s health loop that calls `logsource.IsWatchdogUnhealthy` and restarts the exporter context if a backend becomes unhealthy.

## Environment variables

The binary itself does not read custom env vars, but the Docker log source honors the standard Docker client variables (`DOCKER_HOST`, `DOCKER_TLS_VERIFY`, `DOCKER_CERT_PATH`, etc.). Kubernetes mode uses in-cluster config when available, otherwise `--kubernetes.kubeconfig` (default `~/.kube/config`).

## Dependencies

Key Go modules (see `go.mod` for the authoritative list):

- `github.com/prometheus/client_golang`, `prometheus/client_model`, `prometheus/common`, `prometheus/exporter-toolkit` — metrics, logging, and HTTP server toolkit.
- `github.com/alecthomas/kingpin/v2` — CLI parsing.
- `github.com/nxadm/tail` — file tailing.
- `github.com/coreos/go-systemd/v22` — systemd journal access (CGO).
- `github.com/docker/docker` — Docker log streaming.
- `k8s.io/client-go`, `k8s.io/api`, `k8s.io/apimachinery` — Kubernetes log following.
- `github.com/stretchr/testify` — test assertions.

## Conventions

- Build tags `nosystemd`, `nodocker`, `nokubernetes` gate optional backends. New backend code must register its factory via a build-tagged `init()` and implement `logsource.LogSourceFactory` (including the unexported `requireEmbed()` marker to force embedding `LogSourceFactoryDefaults`).
- Version metadata is injected at build time with `-ldflags "-X main.version=... -X main.commit=... -X main.date=... -X main.builtBy=..."`. The embedded `VERSION` file is only a fallback.
- Tests follow standard Go conventions: `*_test.go` next to the code under test, using `testify`. The `-race` flag is required in CI.
- Lint runs through `pre-commit`; install hooks with `make lint` before pushing changes.
- Releases are produced by GoReleaser (`.goreleaser.yml`) into four flavors: minimal, `_docker`, `_systemd`, and `_aio` (everything). Container images are signed and published to `ghcr.io/hsn723/postfix_exporter`.
- Postfix 2.x is no longer supported; the last release supporting it is `0.14.0`.

## Paubox notes

- The fork is mirrored from upstream; before adding patches, check whether the change can be contributed upstream first.
- When deploying via Helm, prefer the published chart in `charts/postfix-exporter/` rather than maintaining a Paubox-internal copy.
