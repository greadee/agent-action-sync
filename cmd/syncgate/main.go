package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"syncgate/internal/api"
	"syncgate/internal/config"
	"syncgate/internal/core"
	"syncgate/internal/daemon"
	syncengine "syncgate/internal/sync"
	"syncgate/internal/transfer"
	tcptls "syncgate/internal/transport/tcp"
)

func main() {
	if len(os.Args) < 2 {
		runCheckConfig([]string{})
		return
	}

	switch os.Args[1] {
	case "check-config":
		runCheckConfig(os.Args[2:])
	case "diagnostics":
		runDiagnostics(os.Args[2:])
	case "receive-once":
		runReceiveOnce(os.Args[2:])
	case "send-once":
		runSendOnce(os.Args[2:])
	case "status-server":
		runStatusServer(os.Args[2:])
	case "daemon":
		runDaemon(os.Args[2:])
	default:
		exitf("unknown command %q", os.Args[1])
	}
}

func runDaemon(args []string) {
	flags := flag.NewFlagSet("daemon", flag.ExitOnError)
	configPath := flags.String("config", "config.example.json", "path to syncgate JSON config")
	_ = flags.Parse(args)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := daemon.RunConfig(ctx, *configPath, daemon.Options{}); err != nil {
		exitf("%v", err)
	}
}

func runStatusServer(args []string) {
	flags := flag.NewFlagSet("status-server", flag.ExitOnError)
	listen := flags.String("listen", "127.0.0.1:47820", "loopback status server address")
	_ = flags.Parse(args)
	server, err := api.NewHealthServer(*listen, time.Now().UTC())
	if err != nil {
		exitf("%v", err)
	}
	fmt.Printf("syncgate status server on %s\n", *listen)
	if err := server.ListenAndServe(); err != nil {
		exitf("%v", err)
	}
}

func runCheckConfig(args []string) {
	flags := flag.NewFlagSet("check-config", flag.ExitOnError)
	configPath := flags.String("config", "config.example.json", "path to syncgate JSON config")
	_ = flags.Parse(args)

	cfg, err := config.LoadFile(context.Background(), *configPath)
	if err != nil {
		exitf("%v", err)
	}

	fmt.Printf("syncgate config ok: device=%s shares=%d\n", cfg.DeviceName, len(cfg.Shares))
}

func runDiagnostics(args []string) {
	flags := flag.NewFlagSet("diagnostics", flag.ExitOnError)
	reportPath := flags.String("file", "", "path to a local diagnostics JSON snapshot")
	recent := flags.Int("recent", 5, "maximum recent scans to print")
	_ = flags.Parse(args)
	if *reportPath == "" {
		exitf("--file is required")
	}
	if *recent < 1 {
		exitf("--recent must be positive")
	}

	raw, err := os.ReadFile(*reportPath)
	if err != nil {
		exitf("read diagnostics: %v", err)
	}
	var report syncengine.DiagnosticReport
	if err := json.Unmarshal(raw, &report); err != nil {
		exitf("parse diagnostics JSON: %v", err)
	}
	printDiagnostics(os.Stdout, report, *recent)
}

func printDiagnostics(writer io.Writer, report syncengine.DiagnosticReport, recent int) {
	fmt.Fprintf(writer, "syncgate diagnostics generated_at=%s\n", formatDiagnosticTime(report.GeneratedAt))
	fmt.Fprintln(writer, "recent_scans:")
	scans := report.RecentScans
	if len(scans) > recent {
		scans = scans[len(scans)-recent:]
	}
	if len(scans) == 0 {
		fmt.Fprintln(writer, "  none")
	}
	for _, scan := range scans {
		status := "idle"
		switch {
		case scan.Unavailable:
			status = "unavailable"
		case scan.Blocked:
			status = "blocked"
		case scan.Committed:
			status = "committed"
		case scan.Skipped:
			status = "skipped"
		case scan.Started:
			status = "started"
		}
		fmt.Fprintf(writer, "  share=%s trigger=%s status=%s revisions=%d tombstones=%d finished_at=%s\n",
			scan.ShareID, scan.Trigger, status, scan.Revisions, scan.Tombstones, formatDiagnosticTime(scan.FinishedAt))
		for _, reason := range scan.Reasons {
			fmt.Fprintf(writer, "    reason=%q\n", reason)
		}
		if scan.Error != "" {
			fmt.Fprintf(writer, "    error=%q\n", scan.Error)
		}
	}

	fmt.Fprintln(writer, "pending_or_blocked_work:")
	if len(report.Work) == 0 {
		fmt.Fprintln(writer, "  none")
	}
	for _, work := range report.Work {
		fmt.Fprintf(writer, "  job=%s transfer=%s share=%s state=%s retries=%d path=%q next_attempt_at=%s\n",
			work.JobID, work.TransferID, work.ShareID, work.State, work.RetryCount, work.RelativePath, formatDiagnosticTime(work.NextAttemptAt))
		if work.LastError != "" {
			fmt.Fprintf(writer, "    last_error=%q\n", work.LastError)
		}
	}

	fmt.Fprintln(writer, "ignored_paths:")
	if len(report.IgnoredPaths) == 0 {
		fmt.Fprintln(writer, "  none")
	}
	for _, ignored := range report.IgnoredPaths {
		fmt.Fprintf(writer, "  share=%s path=%q pattern=%q\n", ignored.ShareID, ignored.RelativePath, ignored.Pattern)
	}
}

func formatDiagnosticTime(value time.Time) string {
	if value.IsZero() {
		return "-"
	}
	return value.UTC().Format(time.RFC3339Nano)
}

func runReceiveOnce(args []string) {
	flags := flag.NewFlagSet("receive-once", flag.ExitOnError)
	listen := flags.String("listen", "127.0.0.1:47821", "manual TCP/TLS listen address")
	shareRoot := flags.String("share-root", "", "destination share root")
	deviceID := flags.String("device-id", "RECEIVER", "local device ID for development TLS")
	_ = flags.Parse(args)
	if *shareRoot == "" {
		exitf("--share-root is required")
	}

	tlsConfig, err := tcptls.DevTLSConfig(core.DeviceID(*deviceID), true)
	if err != nil {
		exitf("%v", err)
	}
	server := tcptls.New(*listen, tlsConfig)
	ctx := context.Background()
	sessions, err := server.Listen(ctx)
	if err != nil {
		exitf("%v", err)
	}
	defer server.Close()
	fmt.Printf("syncgate receiving on %s\n", server.Address)

	session, ok := <-sessions
	if !ok {
		exitf("listener closed before receiving a session")
	}
	defer session.Close()
	stream, err := session.AcceptStream(ctx)
	if err != nil {
		exitf("%v", err)
	}
	result, err := transfer.ReceiveFile(stream, *shareRoot)
	if err != nil {
		exitf("%v", err)
	}
	fmt.Printf("received %s bytes=%d path=%s\n", result.Manifest.RelativePath, result.BytesReceived, result.DestinationPath)
}

func runSendOnce(args []string) {
	flags := flag.NewFlagSet("send-once", flag.ExitOnError)
	addr := flags.String("addr", "127.0.0.1:47821", "manual TCP/TLS receiver address")
	source := flags.String("file", "", "source file to send")
	relativePath := flags.String("relative-path", "", "destination relative path inside receiver share")
	deviceID := flags.String("device-id", "SENDER", "local device ID for development TLS")
	chunkSize := flags.Int64("chunk-size", transfer.DefaultChunkSize, "fixed transfer chunk size")
	_ = flags.Parse(args)
	if *source == "" {
		exitf("--file is required")
	}
	if *relativePath == "" {
		exitf("--relative-path is required")
	}

	tlsConfig, err := tcptls.DevTLSConfig(core.DeviceID(*deviceID), false)
	if err != nil {
		exitf("%v", err)
	}
	client := tcptls.New(*addr, tlsConfig)
	session, err := client.Connect(context.Background(), "")
	if err != nil {
		exitf("%v", err)
	}
	defer session.Close()
	stream, err := session.OpenStream(context.Background())
	if err != nil {
		exitf("%v", err)
	}
	result, err := transfer.SendFile(stream, *source, *relativePath, *chunkSize)
	if err != nil {
		exitf("%v", err)
	}
	fmt.Printf("sent %s bytes=%d hash=%s\n", result.Manifest.RelativePath, result.BytesSent, result.Manifest.ContentHash)
}

func exitf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "syncgate: "+format+"\n", args...)
	os.Exit(1)
}
