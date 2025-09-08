package main

import (
	"context"
	"log"
	"net/http"
	"os"

	_ "embed"

	"github.com/alecthomas/kingpin/v2"
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
)

func main() {
	var (
		ctx                 = context.Background()
		app                 = kingpin.New("postfix_exporter", "Prometheus metrics exporter for postfix")
		versionFlag         = app.Flag("version", "Print version information").Bool()
		toolkitFlags        = kingpinflag.AddFlags(app, ":9154")
		metricsPath         = app.Flag("web.telemetry-path", "Path under which to expose metrics.").Default("/metrics").String()
		postfixShowqPath    = app.Flag("postfix.showq_path", "Path at which Postfix places its showq socket.").Default("/var/spool/postfix/public/showq").String()
		logUnsupportedLines = app.Flag("log.unsupported", "Log all unsupported lines.").Bool()

		cleanupLabels = app.Flag("postfix.cleanup_service_label", "User-defined service labels for the cleanup service.").Default("cleanup").Strings()
		lmtpLabels    = app.Flag("postfix.lmtp_service_label", "User-defined service labels for the lmtp service.").Default("lmtp").Strings()
		pipeLabels    = app.Flag("postfix.pipe_service_label", "User-defined service labels for the pipe service.").Default("pipe").Strings()
		qmgrLabels    = app.Flag("postfix.qmgr_service_label", "User-defined service labels for the qmgr service.").Default("qmgr").Strings()
		smtpLabels    = app.Flag("postfix.smtp_service_label", "User-defined service labels for the smtp service.").Default("smtp").Strings()
		smtpdLabels   = app.Flag("postfix.smtpd_service_label", "User-defined service labels for the smtpd service.").Default("smtpd").Strings()
		bounceLabels  = app.Flag("postfix.bounce_service_label", "User-defined service labels for the bounce service.").Default("bounce").Strings()
		virtualLabels = app.Flag("postfix.virtual_service_label", "User-defined service labels for the virtual service.").Default("virtual").Strings()
	)

	InitLogSourceFactories(app)
	kingpin.MustParse(app.Parse(os.Args[1:]))

	if version == "" {
		version = fallbackVersion
	}

	if *versionFlag {
		os.Stdout.WriteString(version)
		os.Exit(0)
	}
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
	log.Print(versionString)

	logSrcs, err := NewLogSourceFromFactories(ctx)
	if err != nil {
		log.Fatalf("Error opening log source: %s", err)
	}
	defer func() {
		for _, ls := range logSrcs {
			ls.Close()
		}
	}()
	exporters := make([]*PostfixExporter, 0, len(logSrcs))
	for _, logSrc := range logSrcs {
		exporter := NewPostfixExporter(
			*postfixShowqPath,
			logSrc,
			*logUnsupportedLines,
			WithCleanupLabels(*cleanupLabels),
			WithLmtpLabels(*lmtpLabels),
			WithPipeLabels(*pipeLabels),
			WithQmgrLabels(*qmgrLabels),
			WithSmtpLabels(*smtpLabels),
			WithSmtpdLabels(*smtpdLabels),
			WithBounceLabels(*bounceLabels),
			WithVirtualLabels(*virtualLabels),
		)
		prometheus.MustRegister(exporter)
		exporters = append(exporters, exporter)
	}

	http.Handle(*metricsPath, promhttp.Handler())
	lc := web.LandingConfig{
		Name:        "Postfix Exporter",
		Description: "Prometheus exporter for postfix metrics",
		Version:     versionString,
		Links: []web.LandingLinks{
			{
				Address: *metricsPath,
				Text:    "Metrics",
			},
		},
	}
	lp, err := web.NewLandingPage(lc)
	if err != nil {
		log.Fatalf("Failed to create landing page: %s", err)
	}
	http.Handle("/", lp)

	ctx, cancelFunc := context.WithCancel(ctx)
	defer cancelFunc()
	for _, exporter := range exporters {
		go exporter.StartMetricCollection(ctx)
	}

	server := &http.Server{}
	logger := promslog.New(&promslog.Config{})
	log.Fatal(web.ListenAndServe(server, toolkitFlags, logger))
}
