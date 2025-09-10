[![GitHub release](https://img.shields.io/github/release/hsn723/postfix_exporter.svg?sort=semver&maxAge=60)](https://github.com/hsn723/postfix_exporter/releases)
[![main](https://github.com/Hsn723/postfix_exporter/actions/workflows/main.yml/badge.svg?branch=master)](https://github.com/Hsn723/postfix_exporter/actions/workflows/main.yml)
[![PkgGoDev](https://pkg.go.dev/badge/github.com/hsn723/postfix_exporter?tab=overview)](https://pkg.go.dev/github.com/hsn723/postfix_exporter?tab=overview)
[![Go Report Card](https://goreportcard.com/badge/github.com/hsn723/postfix_exporter)](https://goreportcard.com/report/github.com/hsn723/postfix_exporter)
![GitHub go.mod Go version](https://img.shields.io/github/go-mod/go-version/hsn723/postfix_exporter)

# Prometheus Postfix exporter

Prometheus metrics exporter for [the Postfix mail server](http://www.postfix.org/).
This exporter provides histogram metrics for the size and age of messages stored in
the mail queue. It extracts these metrics from Postfix by connecting to
a UNIX socket under `/var/spool`. It also counts events by parsing Postfix's
log entries, using regular expression matching. The log entries are retrieved from
the systemd journal, the Docker logs, or from a log file.

## Options

These options can be used when starting the `postfix_exporter`

| Flag                     | Description                                          | Default                           |
|--------------------------|------------------------------------------------------|-----------------------------------|
| `--web.listen-address`   | Address to listen on for web interface and telemetry | `9154`                            |
| `--web.config.file   `   | Path to configuration file that can enable TLS or authentication [(ref)](https://github.com/prometheus/exporter-toolkit/blob/master/docs/web-configuration.md). | `""`                            |
| `--web.telemetry-path`   | Path under which to expose metrics                   | `/metrics`                        |
| `--postfix.showq_path`    | Path at which Postfix places its showq socket       | `/var/spool/postfix/public/showq` |
| `--postfix.showq_port`    | TCP port at which showq is listening                | `10025`                           |
| `--postfix.showq_network` | Network protocol to use to connect to showq         | `"unix"`                          |
| `--postfix.logfile_path` | Path where Postfix writes log entries                | `/var/log/mail.log`               |
| `--postfix.logfile_must_exist` | Fail if the log file doesn't exist.            | `true`                            |
| `--postfix.logfile_debug` | Enable debug logging for the log file.              | `false`                           |
| `--postfix.cleanup_service_label` | User-defined service labels for the cleanup service. | `cleanup`                |
| `--postfix.lmtp_service_label`    | User-defined service labels for the lmtp service.    | `lmtp`                   |
| `--postfix.pipe_service_label`    | User-defined service labels for the pipe service.    | `pipe`                   |
| `--postfix.qmgr_service_label`    | User-defined service labels for the qmgr service.    | `qmgr`                   |
| `--postfix.smtp_service_label`    | User-defined service labels for the smtp service.    | `smtp`                   |
| `--postfix.smtpd_service_label`   | User-defined service labels for the smtpd service.   | `smtpd`                  |
| `--postfix.bounce_service_label`  | User-defined service labels for the bounce service.  | `bounce`                 |
| `--postfix.virtual_service_label` | User-defined service labels for the virtual service. | `virtual`                |
| `--log.unsupported`      | Log all unsupported lines                            | `false`                           |
| `--docker.enable`        | Read from the Docker logs instead of a file          | `false`                           |
| `--docker.container.id`  | The container to read Docker logs from               | `postfix`                         |
| `--systemd.enable`       | Read from the systemd journal instead of file        | `false`                           |
| `--systemd.unit`         | Name of the Postfix systemd unit                     | `postfix.service`                 |
| `--systemd.slice`        | Name of the Postfix systemd slice.                   | `""`                              |
| `--systemd.journal_path` | Path to the systemd journal                          | `""`                              |

The `--docker.*` flags are not available for binaries built with the `nodocker` build tag. The `--systemd.*` flags are not available for binaries built with the `nosystemd` build tag.

### User-defined service labels

In postfix, services can be configured multiple times and appear with labels that do not match  their service types. For instance, all the services defined below are valid services of type `smtp` having different labels.

```sh
# master.cf
smtp      unix  -       -       n       -       -       smtp
relay     unix  -       -       n       -       -       smtp
        -o syslog_name=postfix/relay/smtp
encrypt   unix  -       -       n       -       -       smtp
        -o smtp_tls_security_level=encrypt
...
```

User-defined service labels, not service types show up in logs. It is therefore necessary to indicate to postfix_exporter how those service labels are mapped to their relevant service type. This can be done with the `--postfix.${SERVICE_TYPE}_service_labels` command-line flags.

For instance, for the above `master.cf` example postfix_exporter should be called with all the relevant service labels defined. For example:

```sh
./postfix_exporter --postfix.smtp_service_label smtp \
                   --postfix.smtp_service_label relay/smtp \
                   --postfix.smtp_service_label encrypt
```

## (experimental) Connecting to remote showq instances

Instead of connecting to a local socket to extract metrics from a local showq instance, postfix_exporter can connect to a remote showq instance via TCP. Exposing a TCP port for the showq service can be dangerous and extreme caution must be taken to avoid unintentional/unauthorized access to showq, as this will expose sensitive information.

## Events from Docker

If postfix_exporter is built with docker support, postfix servers running in a [Docker](https://www.docker.com/)
container can be monitored using the `--docker.enable` flag. The
default container ID is `postfix`, but can be customized with the
`--docker.container.id` flag.

The default is to connect to the local Docker, but this can be
customized using [the `DOCKER_HOST` and
similar](https://pkg.go.dev/github.com/docker/docker/client?tab=doc#NewEnvClient)
environment variables.

## Events from log file

The log file is tailed when processed. Rotating the log files while the exporter
is running is OK. The path to the log file is specified with the
`--postfix.logfile_path` flag.

## Events from systemd

Retrieval from the systemd journal is enabled with the `--systemd.enable` flag.
This overrides the log file setting.
It is possible to specify the unit (with `--systemd.unit`) or slice (with `--systemd.slice`).
Additionally, it is possible to read the journal from a directory with the `--systemd.journal_path` flag.

## Build options

By default, the exporter is built without docker and systemd support.

```sh
go build -tags nosystemd,nodocker
```

To build the exporter with support for docker or systemd, remove the relevant build build tag from the build arguments. Note that systemd headers are required for building with systemd. On Debian-based systems, this is typically achieved by installing the `libsystemd-dev` APT package.

```
go build -tags nosystemd
```

## Releases

Signed container images are provided from the GitHub Container Registry (https://github.com/Hsn723/postfix_exporter/pkgs/container/postfix_exporter). The binary included in container images is built without docker and systemd support.

The [Releases](https://github.com/Hsn723/postfix_exporter/releases) page includes signed pre-built binaries for various configurations.

- postfix_exporter binaries are minimal builds (docker and systemd support excluded)
- postfix_exporter_docker binaries have docker support built-in
- postfix_exporter_systemd binaries have systemd support built-in
- postfix_exporter_aio binaries are built with everything included, which can be useful for packaging for systems where the final use-case is not known in advance
