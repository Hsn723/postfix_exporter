// Copyright 2017 Kumina, https://kumina.nl/
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"context"
	"errors"
	"log"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/hsn723/postfix_exporter/showq"
	"github.com/prometheus/client_golang/prometheus"
)

var (
	postfixUpDesc = prometheus.NewDesc(
		prometheus.BuildFQName("postfix", "", "up"),
		"Whether scraping Postfix's metrics was successful.",
		[]string{"path"}, nil)

	SystemdNoMoreEntries = errors.New("No more journal entries")
)

// PostfixExporter holds the state that should be preserved by the
// Postfix Prometheus metrics exporter across scrapes.
type PostfixExporter struct {
	qmgrInsertsSize   prometheus.Histogram
	virtualDelivered  prometheus.Counter
	bounceNonDelivery prometheus.Counter

	smtpConnectionTimedOut prometheus.Counter
	// same as smtpProcesses{status=deferred}, kept for compatibility
	smtpStatusDeferred prometheus.Counter
	// should be the same as smtpProcesses{status=deferred}, kept for compatibility, but this doesn't work !
	smtpDeferreds prometheus.Counter

	smtpdSASLAuthenticationFailures prometheus.Counter
	smtpdFCrDNSErrors               prometheus.Counter
	smtpdDisconnects                prometheus.Counter
	smtpdConnects                   prometheus.Counter

	cleanupProcesses   prometheus.Counter
	cleanupRejects     prometheus.Counter
	cleanupNotAccepted prometheus.Counter

	qmgrExpires      prometheus.Counter
	qmgrRemoves      prometheus.Counter
	qmgrInsertsNrcpt prometheus.Histogram

	logSrc LogSource

	smtpdLostConnections *prometheus.CounterVec
	smtpDeferredDSN      *prometheus.CounterVec
	smtpdProcesses       *prometheus.CounterVec
	smtpdRejects         *prometheus.CounterVec
	smtpdTLSConnects     *prometheus.CounterVec

	lmtpDelays *prometheus.HistogramVec
	pipeDelays *prometheus.HistogramVec

	smtpDelays      *prometheus.HistogramVec
	smtpTLSConnects *prometheus.CounterVec
	smtpBouncedDSN  *prometheus.CounterVec
	smtpProcesses   *prometheus.CounterVec

	opendkimSignatureAdded *prometheus.CounterVec

	unsupportedLogEntries *prometheus.CounterVec

	showq     *showq.Showq
	showqPath string

	bounceLabels  []string
	cleanupLabels []string
	smtpLabels    []string
	smtpdLabels   []string
	virtualLabels []string
	qmgrLabels    []string
	pipeLabels    []string
	lmtpLabels    []string

	once sync.Once

	logUnsupportedLines bool
}

// ServiceLabel is a function to apply user-defined service labels to PostfixExporter.
type ServiceLabel func(*PostfixExporter)

// A LogSource is an interface to read log lines.
type LogSource interface {
	// Path returns a representation of the log location.
	Path() string

	// Read returns the next log line. Returns `io.EOF` at the end of
	// the log.
	Read(context.Context) (string, error)

	ConstLabels() prometheus.Labels
	requireEmbed()
}

type LogSourceDefaults struct{}

func (LogSourceDefaults) ConstLabels() prometheus.Labels {
	return prometheus.Labels{}
}
func (LogSourceDefaults) requireEmbed() {}

// Patterns for parsing log messages.
var (
	logLine                             = regexp.MustCompile(` ?(postfix|opendkim)(/([\w_\.+/-]+))?\[\d+\]: ((?:(warning|error|fatal|panic): )?.*)`)
	lmtpPipeSMTPLine                    = regexp.MustCompile(`, relay=(\S+), .*, delays=([0-9\.]+)/([0-9\.]+)/([0-9\.]+)/([0-9\.]+), `)
	qmgrInsertLine                      = regexp.MustCompile(`:.*, size=(\d+), nrcpt=(\d+) `)
	qmgrExpiredLine                     = regexp.MustCompile(`:.*, status=(expired|force-expired), returned to sender`)
	smtpStatusLine                      = regexp.MustCompile(`, status=(\w+) `)
	smtpDSNLine                         = regexp.MustCompile(`, dsn=(\d\.\d+\.\d+)`)
	smtpTLSLine                         = regexp.MustCompile(`^(\S+) TLS connection established to \S+: (\S+) with cipher (\S+) \((\d+)/(\d+) bits\)`)
	smtpConnectionTimedOut              = regexp.MustCompile(`^connect\s+to\s+(.*)\[(.*)\]:(\d+):\s+(Connection timed out)$`)
	smtpdFCrDNSErrorsLine               = regexp.MustCompile(`^warning: hostname \S+ does not resolve to address `)
	smtpdProcessesSASLLine              = regexp.MustCompile(`: client=.*, sasl_method=(\S+)`)
	smtpdRejectsLine                    = regexp.MustCompile(`^NOQUEUE: reject: RCPT from \S+: ([0-9]+) `)
	smtpdLostConnectionLine             = regexp.MustCompile(`^(?:NOQUEUE: )?lost connection after (\w+) from `)
	smtpdSASLAuthenticationFailuresLine = regexp.MustCompile(`^warning: \S+: SASL \S+ authentication failed: `)
	smtpdTLSLine                        = regexp.MustCompile(`^(\S+) TLS connection established from \S+: (\S+) with cipher (\S+) \((\d+)/(\d+) bits\)`)
	opendkimSignatureAdded              = regexp.MustCompile(`^[\w\d]+: DKIM-Signature field added \(s=(\w+), d=(.*)\)$`)
	bounceNonDeliveryLine               = regexp.MustCompile(`: sender non-delivery notification: `)
)

func (e *PostfixExporter) collectFromPostfixLogLine(line, subprocess, level, remainder string) {
	switch {
	case slices.Contains(e.cleanupLabels, subprocess):
		e.collectCleanupLog(line, remainder, level)
	case slices.Contains(e.lmtpLabels, subprocess):
		e.collectLMTPLog(line, remainder, level)
	case slices.Contains(e.pipeLabels, subprocess):
		e.collectPipeLog(line, remainder, level)
	case slices.Contains(e.qmgrLabels, subprocess):
		e.collectQmgrLog(line, remainder, level)
	case slices.Contains(e.smtpLabels, subprocess):
		e.collectSMTPLog(line, remainder, level)
	case slices.Contains(e.smtpdLabels, subprocess):
		e.collectSMTPdLog(line, remainder, level)
	case slices.Contains(e.bounceLabels, subprocess):
		e.collectBounceLog(line, remainder, level)
	case slices.Contains(e.virtualLabels, subprocess):
		e.collectVirtualLog(line, remainder, level)
	default:
		e.addToUnsupportedLine(line, subprocess, level)
	}
}

func (e *PostfixExporter) collectCleanupLog(line, remainder, level string) {
	switch {
	case strings.Contains(remainder, ": message-id=<"):
		e.cleanupProcesses.Inc()
	case strings.Contains(remainder, ": reject: "):
		e.cleanupRejects.Inc()
	default:
		e.addToUnsupportedLine(line, "cleanup", level)
	}
}

func (e *PostfixExporter) collectLMTPLog(line, remainder, level string) {
	lmtpMatches := lmtpPipeSMTPLine.FindStringSubmatch(remainder)
	if lmtpMatches == nil {
		e.addToUnsupportedLine(line, "lmtp", level)
		return
	}
	addToHistogramVec(e.lmtpDelays, lmtpMatches[2], "LMTP pdelay", "before_queue_manager")
	addToHistogramVec(e.lmtpDelays, lmtpMatches[3], "LMTP adelay", "queue_manager")
	addToHistogramVec(e.lmtpDelays, lmtpMatches[4], "LMTP sdelay", "connection_setup")
	addToHistogramVec(e.lmtpDelays, lmtpMatches[5], "LMTP xdelay", "transmission")
}

func (e *PostfixExporter) collectPipeLog(line, remainder, level string) {
	pipeMatches := lmtpPipeSMTPLine.FindStringSubmatch(remainder)
	if pipeMatches == nil {
		e.addToUnsupportedLine(line, "pipe", level)
		return
	}
	addToHistogramVec(e.pipeDelays, pipeMatches[2], "PIPE pdelay", pipeMatches[1], "before_queue_manager")
	addToHistogramVec(e.pipeDelays, pipeMatches[3], "PIPE adelay", pipeMatches[1], "queue_manager")
	addToHistogramVec(e.pipeDelays, pipeMatches[4], "PIPE sdelay", pipeMatches[1], "connection_setup")
	addToHistogramVec(e.pipeDelays, pipeMatches[5], "PIPE xdelay", pipeMatches[1], "transmission")
}

func (e *PostfixExporter) collectQmgrLog(line, remainder, level string) {
	qmgrInsertMatches := qmgrInsertLine.FindStringSubmatch(remainder)
	switch {
	case qmgrInsertMatches != nil:
		addToHistogram(e.qmgrInsertsSize, qmgrInsertMatches[1], "QMGR size")
		addToHistogram(e.qmgrInsertsNrcpt, qmgrInsertMatches[2], "QMGR nrcpt")
	case strings.HasSuffix(remainder, ": removed"):
		e.qmgrRemoves.Inc()
	case qmgrExpiredLine.MatchString(remainder):
		e.qmgrExpires.Inc()
	default:
		e.addToUnsupportedLine(line, "qmgr", level)
	}
}

func (e *PostfixExporter) collectSMTPLog(line, remainder, level string) {
	if smtpMatches := lmtpPipeSMTPLine.FindStringSubmatch(remainder); smtpMatches != nil {
		addToHistogramVec(e.smtpDelays, smtpMatches[2], "before_queue_manager", "")
		addToHistogramVec(e.smtpDelays, smtpMatches[3], "queue_manager", "")
		addToHistogramVec(e.smtpDelays, smtpMatches[4], "connection_setup", "")
		addToHistogramVec(e.smtpDelays, smtpMatches[5], "transmission", "")
		e.collectSMTPStatusLog(remainder)
	} else if smtpTLSMatches := smtpTLSLine.FindStringSubmatch(remainder); smtpTLSMatches != nil {
		e.smtpTLSConnects.WithLabelValues(smtpTLSMatches[1:]...).Inc()
	} else if connectionTimedOutMatches := smtpConnectionTimedOut.FindStringSubmatch(remainder); connectionTimedOutMatches != nil {
		e.smtpConnectionTimedOut.Inc()
	} else {
		e.addToUnsupportedLine(line, "smtp", level)
	}
}

func (e *PostfixExporter) collectSMTPStatusLog(remainder string) {
	smtpStatusMatches := smtpStatusLine.FindStringSubmatch(remainder)
	if smtpStatusMatches == nil {
		return
	}
	e.smtpProcesses.WithLabelValues(smtpStatusMatches[1]).Inc()
	dsnMatches := smtpDSNLine.FindStringSubmatch(remainder)
	switch smtpStatusMatches[1] {
	case "deferred":
		e.smtpStatusDeferred.Inc()
		if dsnMatches != nil {
			e.smtpDeferredDSN.WithLabelValues(dsnMatches[1]).Inc()
		}
	case "bounced":
		if dsnMatches != nil {
			e.smtpBouncedDSN.WithLabelValues(dsnMatches[1]).Inc()
		}
	}
}

func (e *PostfixExporter) collectSMTPdLog(line, remainder, level string) {
	if strings.HasPrefix(remainder, "connect from ") {
		e.smtpdConnects.Inc()
	} else if strings.HasPrefix(remainder, "disconnect from ") {
		e.smtpdDisconnects.Inc()
	} else if smtpdFCrDNSErrorsLine.MatchString(remainder) {
		e.smtpdFCrDNSErrors.Inc()
	} else if smtpdLostConnectionMatches := smtpdLostConnectionLine.FindStringSubmatch(remainder); smtpdLostConnectionMatches != nil {
		e.smtpdLostConnections.WithLabelValues(smtpdLostConnectionMatches[1]).Inc()
	} else if smtpdProcessesSASLMatches := smtpdProcessesSASLLine.FindStringSubmatch(remainder); smtpdProcessesSASLMatches != nil {
		e.smtpdProcesses.WithLabelValues(strings.ReplaceAll(smtpdProcessesSASLMatches[1], ",", "")).Inc()
	} else if strings.Contains(remainder, ": client=") {
		e.smtpdProcesses.WithLabelValues("NONE").Inc()
	} else if smtpdRejectsMatches := smtpdRejectsLine.FindStringSubmatch(remainder); smtpdRejectsMatches != nil {
		e.smtpdRejects.WithLabelValues(smtpdRejectsMatches[1]).Inc()
	} else if smtpdSASLAuthenticationFailuresLine.MatchString(remainder) {
		e.smtpdSASLAuthenticationFailures.Inc()
	} else if smtpdTLSMatches := smtpdTLSLine.FindStringSubmatch(remainder); smtpdTLSMatches != nil {
		e.smtpdTLSConnects.WithLabelValues(smtpdTLSMatches[1:]...).Inc()
	} else {
		e.addToUnsupportedLine(line, "smtpd", level)
	}
}

func (e *PostfixExporter) collectBounceLog(line, remainder, level string) {
	bounceMatches := bounceNonDeliveryLine.FindStringSubmatch(remainder)
	if bounceMatches == nil {
		e.addToUnsupportedLine(line, "postfix", level)
		return
	}
	e.bounceNonDelivery.Inc()
}

func (e *PostfixExporter) collectVirtualLog(line, remainder, level string) {
	if strings.HasSuffix(remainder, ", status=sent (delivered to maildir)") {
		e.virtualDelivered.Inc()
	} else {
		e.addToUnsupportedLine(line, "postfix", level)
	}
}

// CollectFromLogline collects metrict from a Postfix log line.
func (e *PostfixExporter) CollectFromLogLine(line string) {
	if line == "" {
		return
	}
	// Strip off timestamp, hostname, etc.
	logMatches := logLine.FindStringSubmatch(line)

	if logMatches == nil {
		// Unknown log entry format.
		e.addToUnsupportedLine(line, "", "")
		return
	}
	process := logMatches[1]
	level := logMatches[5]
	remainder := logMatches[4]
	switch process {
	case "postfix":
		subprocess := logMatches[3]
		e.collectFromPostfixLogLine(line, subprocess, level, remainder)
	case "opendkim":
		if opendkimMatches := opendkimSignatureAdded.FindStringSubmatch(remainder); opendkimMatches != nil {
			e.opendkimSignatureAdded.WithLabelValues(opendkimMatches[1], opendkimMatches[2]).Inc()
		} else {
			e.addToUnsupportedLine(line, process, level)
		}
	default:
		// Unknown log entry format.
		e.addToUnsupportedLine(line, process, level)
	}
}

func (e *PostfixExporter) addToUnsupportedLine(line string, subprocess string, level string) {
	if e.logUnsupportedLines {
		log.Printf("Unsupported Line: %v", line)
	}
	e.unsupportedLogEntries.WithLabelValues(subprocess, level).Inc()
}

func addToHistogram(h prometheus.Histogram, value, fieldName string) {
	float, err := strconv.ParseFloat(value, 64)
	if err != nil {
		log.Printf("Couldn't convert value '%s' for %v: %v", value, fieldName, err)
	}
	h.Observe(float)
}
func addToHistogramVec(h *prometheus.HistogramVec, value, fieldName string, labels ...string) {
	float, err := strconv.ParseFloat(value, 64)
	if err != nil {
		log.Printf("Couldn't convert value '%s' for %v: %v", value, fieldName, err)
	}
	h.WithLabelValues(labels...).Observe(float)
}

var (
	defaultCleanupLabels = []string{"cleanup"}
	defaultLmtpLabels    = []string{"lmtp"}
	defaultPipeLabels    = []string{"pipe"}
	defaultQmgrLabels    = []string{"qmgr"}
	defaultSmtpLabels    = []string{"smtp"}
	defaultSmtpdLabels   = []string{"smtpd"}
	defaultBounceLabels  = []string{"bounce"}
	defaultVirtualLabels = []string{"virtual"}
)

// WithCleanupLabels is a function to apply user-defined service labels to PostfixExporter.
func WithCleanupLabels(labels []string) ServiceLabel {
	return func(e *PostfixExporter) {
		e.cleanupLabels = labels
	}
}

// WithCleanupLabels is a function to apply user-defined service labels to PostfixExporter.
func WithLmtpLabels(labels []string) ServiceLabel {
	return func(e *PostfixExporter) {
		e.lmtpLabels = labels
	}
}

// WithCleanupLabels is a function to apply user-defined service labels to PostfixExporter.
func WithPipeLabels(labels []string) ServiceLabel {
	return func(e *PostfixExporter) {
		e.pipeLabels = labels
	}
}

// WithCleanupLabels is a function to apply user-defined service labels to PostfixExporter.
func WithQmgrLabels(labels []string) ServiceLabel {
	return func(e *PostfixExporter) {
		e.qmgrLabels = labels
	}
}

// WithCleanupLabels is a function to apply user-defined service labels to PostfixExporter.
func WithSmtpLabels(labels []string) ServiceLabel {
	return func(e *PostfixExporter) {
		e.smtpLabels = labels
	}
}

// WithCleanupLabels is a function to apply user-defined service labels to PostfixExporter.
func WithSmtpdLabels(labels []string) ServiceLabel {
	return func(e *PostfixExporter) {
		e.smtpdLabels = labels
	}
}

// WithCleanupLabels is a function to apply user-defined service labels to PostfixExporter.
func WithBounceLabels(labels []string) ServiceLabel {
	return func(e *PostfixExporter) {
		e.bounceLabels = labels
	}
}

// WithCleanupLabels is a function to apply user-defined service labels to PostfixExporter.
func WithVirtualLabels(labels []string) ServiceLabel {
	return func(e *PostfixExporter) {
		e.virtualLabels = labels
	}
}

func (e *PostfixExporter) init() {
	timeBuckets := []float64{1e-3, 1e-2, 1e-1, 1.0, 10, 1 * 60, 1 * 60 * 60, 24 * 60 * 60, 2 * 24 * 60 * 60}

	e.once.Do(func() {
		constLabels := LogSourceDefaults{}.ConstLabels()
		if e.logSrc != nil {
			constLabels = e.logSrc.ConstLabels()
		}
		e.cleanupProcesses = prometheus.NewCounter(prometheus.CounterOpts{
			Namespace:   "postfix",
			Name:        "cleanup_messages_processed_total",
			Help:        "Total number of messages processed by cleanup.",
			ConstLabels: constLabels,
		})
		e.cleanupRejects = prometheus.NewCounter(prometheus.CounterOpts{
			Namespace:   "postfix",
			Name:        "cleanup_messages_rejected_total",
			Help:        "Total number of messages rejected by cleanup.",
			ConstLabels: constLabels,
		})
		e.cleanupNotAccepted = prometheus.NewCounter(prometheus.CounterOpts{
			Namespace:   "postfix",
			Name:        "cleanup_messages_not_accepted_total",
			Help:        "Total number of messages not accepted by cleanup.",
			ConstLabels: constLabels,
		})
		e.lmtpDelays = prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Namespace:   "postfix",
				Name:        "lmtp_delivery_delay_seconds",
				Help:        "LMTP message processing time in seconds.",
				Buckets:     timeBuckets,
				ConstLabels: constLabels,
			},
			[]string{"stage"})
		e.pipeDelays = prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Namespace:   "postfix",
				Name:        "pipe_delivery_delay_seconds",
				Help:        "Pipe message processing time in seconds.",
				Buckets:     timeBuckets,
				ConstLabels: constLabels,
			},
			[]string{"relay", "stage"})
		e.qmgrInsertsNrcpt = prometheus.NewHistogram(prometheus.HistogramOpts{
			Namespace:   "postfix",
			Name:        "qmgr_messages_inserted_receipients",
			Help:        "Number of receipients per message inserted into the mail queues.",
			Buckets:     []float64{1, 2, 4, 8, 16, 32, 64, 128},
			ConstLabels: constLabels,
		})
		e.qmgrInsertsSize = prometheus.NewHistogram(prometheus.HistogramOpts{
			Namespace:   "postfix",
			Name:        "qmgr_messages_inserted_size_bytes",
			Help:        "Size of messages inserted into the mail queues in bytes.",
			Buckets:     []float64{1e3, 1e4, 1e5, 1e6, 1e7, 1e8, 1e9},
			ConstLabels: constLabels,
		})
		e.qmgrRemoves = prometheus.NewCounter(prometheus.CounterOpts{
			Namespace:   "postfix",
			Name:        "qmgr_messages_removed_total",
			Help:        "Total number of messages removed from mail queues.",
			ConstLabels: constLabels,
		})
		e.qmgrExpires = prometheus.NewCounter(prometheus.CounterOpts{
			Namespace:   "postfix",
			Name:        "qmgr_messages_expired_total",
			Help:        "Total number of messages expired from mail queues.",
			ConstLabels: constLabels,
		})
		e.smtpDelays = prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Namespace:   "postfix",
				Name:        "smtp_delivery_delay_seconds",
				Help:        "SMTP message processing time in seconds.",
				Buckets:     timeBuckets,
				ConstLabels: constLabels,
			},
			[]string{"stage"})
		e.smtpTLSConnects = prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace:   "postfix",
				Name:        "smtp_tls_connections_total",
				Help:        "Total number of outgoing TLS connections.",
				ConstLabels: constLabels,
			},
			[]string{"trust", "protocol", "cipher", "secret_bits", "algorithm_bits"})
		e.smtpDeferreds = prometheus.NewCounter(prometheus.CounterOpts{
			Namespace:   "postfix",
			Name:        "smtp_deferred_messages_total",
			Help:        "Total number of messages that have been deferred on SMTP.",
			ConstLabels: constLabels,
		})
		e.smtpProcesses = prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace:   "postfix",
				Name:        "smtp_messages_processed_total",
				Help:        "Total number of messages that have been processed by the smtp process.",
				ConstLabels: constLabels,
			},
			[]string{"status"})
		e.smtpDeferredDSN = prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace:   "postfix",
				Name:        "smtp_deferred_messages_by_dsn_total",
				Help:        "Total number of messages that have been deferred on SMTP by DSN.",
				ConstLabels: constLabels,
			},
			[]string{"dsn"})
		e.smtpBouncedDSN = prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace:   "postfix",
				Name:        "smtp_bounced_messages_by_dsn_total",
				Help:        "Total number of messages that have been bounced on SMTP by DSN.",
				ConstLabels: constLabels,
			},
			[]string{"dsn"})
		e.smtpConnectionTimedOut = prometheus.NewCounter(prometheus.CounterOpts{
			Namespace:   "postfix",
			Name:        "smtp_connection_timed_out_total",
			Help:        "Total number of messages that have been deferred on SMTP.",
			ConstLabels: constLabels,
		})
		e.smtpdConnects = prometheus.NewCounter(prometheus.CounterOpts{
			Namespace:   "postfix",
			Name:        "smtpd_connects_total",
			Help:        "Total number of incoming connections.",
			ConstLabels: constLabels,
		})
		e.smtpdDisconnects = prometheus.NewCounter(prometheus.CounterOpts{
			Namespace:   "postfix",
			Name:        "smtpd_disconnects_total",
			Help:        "Total number of incoming disconnections.",
			ConstLabels: constLabels,
		})
		e.smtpdFCrDNSErrors = prometheus.NewCounter(prometheus.CounterOpts{
			Namespace:   "postfix",
			Name:        "smtpd_forward_confirmed_reverse_dns_errors_total",
			Help:        "Total number of connections for which forward-confirmed DNS cannot be resolved.",
			ConstLabels: constLabels,
		})
		e.smtpdLostConnections = prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace:   "postfix",
				Name:        "smtpd_connections_lost_total",
				Help:        "Total number of connections lost.",
				ConstLabels: constLabels,
			},
			[]string{"after_stage"})
		e.smtpdProcesses = prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace:   "postfix",
				Name:        "smtpd_messages_processed_total",
				Help:        "Total number of messages processed.",
				ConstLabels: constLabels,
			},
			[]string{"sasl_method"})
		e.smtpdRejects = prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace:   "postfix",
				Name:        "smtpd_messages_rejected_total",
				Help:        "Total number of NOQUEUE rejects.",
				ConstLabels: constLabels,
			},
			[]string{"code"})
		e.smtpdSASLAuthenticationFailures = prometheus.NewCounter(prometheus.CounterOpts{
			Namespace:   "postfix",
			Name:        "smtpd_sasl_authentication_failures_total",
			Help:        "Total number of SASL authentication failures.",
			ConstLabels: constLabels,
		})
		e.smtpdTLSConnects = prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace:   "postfix",
				Name:        "smtpd_tls_connections_total",
				Help:        "Total number of incoming TLS connections.",
				ConstLabels: constLabels,
			},
			[]string{"trust", "protocol", "cipher", "secret_bits", "algorithm_bits"})
		e.unsupportedLogEntries = prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace:   "postfix",
				Name:        "unsupported_log_entries_total",
				Help:        "Log entries that could not be processed.",
				ConstLabels: constLabels,
			},
			[]string{"service", "level"})
		e.smtpStatusDeferred = prometheus.NewCounter(prometheus.CounterOpts{
			Namespace:   "postfix",
			Name:        "smtp_status_deferred",
			Help:        "Total number of messages deferred.",
			ConstLabels: constLabels,
		})
		e.opendkimSignatureAdded = prometheus.NewCounterVec( // deprecated in a future release.
			prometheus.CounterOpts{
				Namespace:   "opendkim",
				Name:        "signatures_added_total",
				Help:        "Total number of messages signed.",
				ConstLabels: constLabels,
			},
			[]string{"subject", "domain"},
		)
		e.bounceNonDelivery = prometheus.NewCounter(prometheus.CounterOpts{
			Namespace:   "postfix",
			Name:        "bounce_non_delivery_notification_total",
			Help:        "Total number of non delivery notification sent by bounce.",
			ConstLabels: constLabels,
		})
		e.virtualDelivered = prometheus.NewCounter(prometheus.CounterOpts{
			Namespace:   "postfix",
			Name:        "virtual_delivered_total",
			Help:        "Total number of mail delivered to a virtual mailbox.",
			ConstLabels: constLabels,
		})
	})
}

// NewPostfixExporter creates a new Postfix exporter instance.
func NewPostfixExporter(showqPath string, logSrc LogSource, logUnsupportedLines bool, serviceLabels ...ServiceLabel) *PostfixExporter {
	constLabels := LogSourceDefaults{}.ConstLabels()
	if logSrc != nil {
		constLabels = logSrc.ConstLabels()
	}
	postfixExporter := &PostfixExporter{
		cleanupLabels:       defaultCleanupLabels,
		lmtpLabels:          defaultLmtpLabels,
		pipeLabels:          defaultPipeLabels,
		qmgrLabels:          defaultQmgrLabels,
		smtpLabels:          defaultSmtpLabels,
		smtpdLabels:         defaultSmtpdLabels,
		bounceLabels:        defaultBounceLabels,
		virtualLabels:       defaultVirtualLabels,
		logUnsupportedLines: logUnsupportedLines,
		showq:               showq.NewShowq(showqPath, constLabels),
		showqPath:           showqPath,
		logSrc:              logSrc,
	}

	for _, serviceLabel := range serviceLabels {
		serviceLabel(postfixExporter)
	}

	postfixExporter.init()

	return postfixExporter
}

// Describe the Prometheus metrics that are going to be exported.
func (e *PostfixExporter) Describe(ch chan<- *prometheus.Desc) {
	ch <- postfixUpDesc

	if e.logSrc == nil {
		return
	}
	ch <- e.cleanupProcesses.Desc()
	ch <- e.cleanupRejects.Desc()
	ch <- e.cleanupNotAccepted.Desc()
	e.lmtpDelays.Describe(ch)
	e.pipeDelays.Describe(ch)
	ch <- e.qmgrInsertsNrcpt.Desc()
	ch <- e.qmgrInsertsSize.Desc()
	ch <- e.qmgrRemoves.Desc()
	ch <- e.qmgrExpires.Desc()
	e.smtpDelays.Describe(ch)
	e.smtpTLSConnects.Describe(ch)
	ch <- e.smtpDeferreds.Desc()
	e.smtpProcesses.Describe(ch)
	e.smtpDeferredDSN.Describe(ch)
	e.smtpBouncedDSN.Describe(ch)
	ch <- e.smtpdConnects.Desc()
	ch <- e.smtpdDisconnects.Desc()
	ch <- e.smtpdFCrDNSErrors.Desc()
	e.smtpdLostConnections.Describe(ch)
	e.smtpdProcesses.Describe(ch)
	e.smtpdRejects.Describe(ch)
	ch <- e.smtpdSASLAuthenticationFailures.Desc()
	e.smtpdTLSConnects.Describe(ch)
	ch <- e.smtpStatusDeferred.Desc()
	e.unsupportedLogEntries.Describe(ch)
	e.smtpConnectionTimedOut.Describe(ch)
	e.opendkimSignatureAdded.Describe(ch)
	ch <- e.bounceNonDelivery.Desc()
	ch <- e.virtualDelivered.Desc()
}

func (e *PostfixExporter) StartMetricCollection(ctx context.Context) {
	if e.logSrc == nil {
		return
	}

	gaugeVec := prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace:   "postfix",
			Subsystem:   "",
			Name:        "up",
			Help:        "Whether scraping Postfix's metrics was successful.",
			ConstLabels: e.logSrc.ConstLabels(),
		},
		[]string{"path"})
	gauge := gaugeVec.WithLabelValues(e.logSrc.Path())
	defer gauge.Set(0)

	for {
		line, err := e.logSrc.Read(ctx)
		if err != nil {
			if err != SystemdNoMoreEntries {
				log.Printf("Couldn't read journal: %v", err)
				return
			}
		}
		e.CollectFromLogLine(line)
		gauge.Set(1)
	}
}

// Collect metrics from Postfix's showq socket and its log file.
func (e *PostfixExporter) Collect(ch chan<- prometheus.Metric) {
	err := e.showq.Collect(ch)
	if err == nil {
		ch <- prometheus.MustNewConstMetric(
			postfixUpDesc,
			prometheus.GaugeValue,
			1.0,
			e.showqPath)
	} else {
		log.Printf("Failed to scrape showq socket: %s", err)
		ch <- prometheus.MustNewConstMetric(
			postfixUpDesc,
			prometheus.GaugeValue,
			0.0,
			e.showqPath)
	}

	if e.logSrc == nil {
		return
	}
	ch <- e.cleanupProcesses
	ch <- e.cleanupRejects
	ch <- e.cleanupNotAccepted
	e.lmtpDelays.Collect(ch)
	e.pipeDelays.Collect(ch)
	ch <- e.qmgrInsertsNrcpt
	ch <- e.qmgrInsertsSize
	ch <- e.qmgrRemoves
	ch <- e.qmgrExpires
	e.smtpDelays.Collect(ch)
	e.smtpTLSConnects.Collect(ch)
	ch <- e.smtpDeferreds
	e.smtpProcesses.Collect(ch)
	ch <- e.smtpdConnects
	ch <- e.smtpdDisconnects
	ch <- e.smtpdFCrDNSErrors
	e.smtpdLostConnections.Collect(ch)
	e.smtpdProcesses.Collect(ch)
	e.smtpDeferredDSN.Collect(ch)
	e.smtpBouncedDSN.Collect(ch)
	e.smtpdRejects.Collect(ch)
	ch <- e.smtpdSASLAuthenticationFailures
	e.smtpdTLSConnects.Collect(ch)
	ch <- e.smtpStatusDeferred
	e.unsupportedLogEntries.Collect(ch)
	ch <- e.smtpConnectionTimedOut
	e.opendkimSignatureAdded.Collect(ch)
	ch <- e.bounceNonDelivery
	ch <- e.virtualDelivered
}
