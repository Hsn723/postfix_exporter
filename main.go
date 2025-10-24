package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"time"

	_ "embed"

	"github.com/alecthomas/kingpin/v2"
	"github.com/hsn723/postfix_exporter/exporter"
	"github.com/hsn723/postfix_exporter/logsource"
	"github.com/hsn723/postfix_exporter/showq"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/prometheus/common/promslog"
	"github.com/prometheus/exporter-toolkit/web"
	"github.com/prometheus/exporter-toolkit/web/kingpinflag"
)

var (
	version string
	commit  string
	date    string
	builtBy string

	//go:embed VERSION
	fallbackVersion string

	app                 *kingpin.Application
	versionFlag         bool
	toolkitFlags        *web.FlagConfig
	metricsPath         string
	postfixShowqPath    string
	postfixShowqPort    int
	postfixShowqNetwork string
	logUnsupportedLines bool

	logLevel  string
	logFormat string
	logConfig *promslog.Config

	cleanupLabels []string
	lmtpLabels    []string
	pipeLabels    []string
	qmgrLabels    []string
	smtpLabels    []string
	smtpdLabels   []string
	bounceLabels  []string
	virtualLabels []string

	useWatchdog bool
)

func getShowqAddress(path, remoteAddr, network string, port int) string {
	switch network {
	case "unix":
		return path
	case "tcp", "tcp4", "tcp6":
		return fmt.Sprintf("%s:%d", remoteAddr, port)
	default:
		logFatal("Unsupported network type", "network", network)
		return ""
	}
}

func buildVersionString() string {
	versionString := "postfix_exporter " + version
	if commit != "" {
		versionString += " (" + commit + ")"
	}
	if date != "" {
		versionString += " built on " + date
	}
	if builtBy != "" {
		versionString += " by: " + builtBy
	}
	return versionString
}

func logFatal(msg string, args ...any) {
	slog.Error(msg, args...)
	os.Exit(1)
}

func initializeExporters(logSrcs []logsource.LogSourceCloser) []*exporter.PostfixExporter {
	exporters := make([]*exporter.PostfixExporter, 0, len(logSrcs))

	for _, logSrc := range logSrcs {
		showqAddr := getShowqAddress(postfixShowqPath, logSrc.RemoteAddr(), postfixShowqNetwork, postfixShowqPort)
		s := showq.NewShowq(showqAddr).WithNetwork(postfixShowqNetwork).WithConstLabels(logSrc.ConstLabels())
		exporter := exporter.NewPostfixExporter(
			s,
			logSrc,
			logUnsupportedLines,
			exporter.WithCleanupLabels(cleanupLabels),
			exporter.WithLmtpLabels(lmtpLabels),
			exporter.WithPipeLabels(pipeLabels),
			exporter.WithQmgrLabels(qmgrLabels),
			exporter.WithSmtpLabels(smtpLabels),
			exporter.WithSmtpdLabels(smtpdLabels),
			exporter.WithBounceLabels(bounceLabels),
			exporter.WithVirtualLabels(virtualLabels),
		)
		prometheus.MustRegister(exporter)
		exporters = append(exporters, exporter)
	}
	return exporters
}

func runExporter(ctx context.Context) {
	logSrcs, err := logsource.NewLogSourceFromFactories(ctx)
	if err != nil {
		logFatal("Error opening log source", "error", err.Error())
	}
	exporters := initializeExporters(logSrcs)

	for _, exporter := range exporters {
		go exporter.StartMetricCollection(ctx)
	}

	go func() {
		<-ctx.Done()
		for _, ls := range logSrcs {
			ls.Close()
		}
		for _, exporter := range exporters {
			prometheus.Unregister(exporter)
		}
	}()
}

func setupMetricsServer(versionString string) error {
	http.Handle(metricsPath, promhttp.Handler())
	lc := web.LandingConfig{
		Name:        "Postfix Exporter",
		Description: "Prometheus exporter for postfix metrics",
		Version:     versionString,
		Links: []web.LandingLinks{
			{
				Address: metricsPath,
				Text:    "Metrics",
			},
		},
	}
	lp, err := web.NewLandingPage(lc)
	if err != nil {
		return err
	}
	http.Handle("/", lp)
	return nil
}

func init() {
	app = kingpin.New("postfix_exporter", "Prometheus metrics exporter for postfix")
	app.Flag("version", "Print version information").BoolVar(&versionFlag)
	app.Flag("watchdog", "Use watchdog to monitor log sources.").Default("false").BoolVar(&useWatchdog)
	toolkitFlags = kingpinflag.AddFlags(app, ":9154")
	app.Flag("web.telemetry-path", "Path under which to expose metrics.").Default("/metrics").StringVar(&metricsPath)
	app.Flag("postfix.showq_path", "Path at which Postfix places its showq socket.").Default("/var/spool/postfix/public/showq").StringVar(&postfixShowqPath)
	app.Flag("postfix.showq_port", "TCP port at which Postfix's showq service is listening.").Default("10025").IntVar(&postfixShowqPort)
	app.Flag("postfix.showq_network", "Network type for Postfix's showq service").Default("unix").StringVar(&postfixShowqNetwork)
	app.Flag("log.unsupported", "Log all unsupported lines.").BoolVar(&logUnsupportedLines)
	app.Flag("log.level", "Log level: debug, info, warn, error").Default("info").StringVar(&logLevel)
	app.Flag("log.format", "Log format: logfmt, json").Default("logfmt").StringVar(&logFormat)

	app.Flag("postfix.cleanup_service_label", "User-defined service labels for the cleanup service.").Default("cleanup").StringsVar(&cleanupLabels)
	app.Flag("postfix.lmtp_service_label", "User-defined service labels for the lmtp service.").Default("lmtp").StringsVar(&lmtpLabels)
	app.Flag("postfix.pipe_service_label", "User-defined service labels for the pipe service.").Default("pipe").StringsVar(&pipeLabels)
	app.Flag("postfix.qmgr_service_label", "User-defined service labels for the qmgr service.").Default("qmgr").StringsVar(&qmgrLabels)
	app.Flag("postfix.smtp_service_label", "User-defined service labels for the smtp service.").Default("smtp").StringsVar(&smtpLabels)
	app.Flag("postfix.smtpd_service_label", "User-defined service labels for the smtpd service.").Default("smtpd").StringsVar(&smtpdLabels)
	app.Flag("postfix.bounce_service_label", "User-defined service labels for the bounce service.").Default("bounce").StringsVar(&bounceLabels)
	app.Flag("postfix.virtual_service_label", "User-defined service labels for the virtual service.").Default("virtual").StringsVar(&virtualLabels)

	logsource.InitLogSourceFactories(app)
	kingpin.MustParse(app.Parse(os.Args[1:]))

	logConfig = &promslog.Config{
		Level:  promslog.NewLevel(),
		Format: promslog.NewFormat(),
	}
	if err := logConfig.Level.Set(logLevel); err != nil {
		logFatal("Invalid log level", "level", logLevel, "error", err.Error())
	}
	if err := logConfig.Format.Set(logFormat); err != nil {
		logFatal("Invalid log format", "format", logFormat, "error", err.Error())
	}
}

func main() {
	ctx := context.Background()
	logger := promslog.New(logConfig)
	slog.SetDefault(logger)

	if version == "" {
		version = fallbackVersion
	}

	if versionFlag {
		os.Stdout.WriteString(version)
		os.Exit(0)
	}
	versionString := buildVersionString()
	slog.Info(versionString)

	if err := setupMetricsServer(versionString); err != nil {
		logFatal("Failed to create landing page", "error", err.Error)
	}

	ctx, cancelFunc := context.WithCancel(ctx)
	defer cancelFunc()
	runExporter(ctx)

	// Start watchdog if enabled
	if useWatchdog {
		go func() {
			ticker := time.NewTicker(5 * time.Second)
			watchdogCtx := context.Background()
			defer ticker.Stop()
			for range ticker.C {
				if logsource.IsWatchdogUnhealthy(watchdogCtx) {
					slog.Warn("Watchdog: log source unhealthy, reloading")
					cancelFunc()
					ctx, cancelFunc = context.WithCancel(context.Background())
					runExporter(ctx)
				}
			}
		}()
	}

	server := &http.Server{}
	if err := web.ListenAndServe(server, toolkitFlags, logger); err != nil {
		logFatal("Error starting HTTP server", "error", err.Error)
	}
}
