package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"sort"
	"strings"
	"syscall"
	"time"

	"syncgate/internal/api"
	"syncgate/internal/config"
	"syncgate/internal/core"
	"syncgate/internal/daemon"
	"syncgate/internal/pairing"
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
	case "daemon":
		runDaemon(os.Args[2:])
	case "identity-migrate":
		runIdentityMigrate(os.Args[2:])
	case "pair-create":
		runPairCreate(os.Args[2:])
	case "pair-inspect":
		runPairInspect(os.Args[2:])
	case "pair-accept":
		runPairAccept(os.Args[2:])
	case "pair-revoke":
		runPairRevoke(os.Args[2:])
	case "daemon-status":
		runDaemonStatus(os.Args[2:])
	case "scan":
		runScan(os.Args[2:])
	case "project-migrate-preflight":
		runProjectMigratePreflight(os.Args[2:])
	case "project-migrate-apply":
		runProjectMigrateApply(os.Args[2:])
	case "job-pause":
		runJobControl(os.Args[2:], syncengine.OneWayJobControlPause)
	case "job-resume":
		runJobControl(os.Args[2:], syncengine.OneWayJobControlResume)
	case "job-retry":
		runJobControl(os.Args[2:], syncengine.OneWayJobControlRetry)
	default:
		exitf("unknown command %q", os.Args[1])
	}
}

func runIdentityMigrate(args []string) {
	flags := flag.NewFlagSet("identity-migrate", flag.ExitOnError)
	configPath := flags.String("config", "config.example.json", "path to syncgate JSON config")
	_ = flags.Parse(args)

	cfg, err := config.LoadFile(context.Background(), *configPath)
	if err != nil {
		exitf("%v", err)
	}
	migrated, err := daemon.MigrateDevelopmentIdentity(cfg)
	if err != nil {
		exitf("%v", err)
	}
	fmt.Printf("syncgate identity migrated: device=%s\n", migrated.DeviceID)
}

func runPairCreate(args []string) {
	flags := flag.NewFlagSet("pair-create", flag.ExitOnError)
	configPath := flags.String("config", "config.example.json", "path to syncgate JSON config")
	ttl := flags.Duration("ttl", 10*time.Minute, "invitation lifetime")
	requestedValue := flags.String("request", "", "advisory comma-separated share capabilities")
	_ = flags.Parse(args)
	requested, err := parsePairingCapabilities(*requestedValue)
	if err != nil {
		exitf("invalid --request: %v", err)
	}

	client := openAdminClient(*configPath)
	defer client.Close()
	capabilities := make([]string, len(requested))
	for index, capability := range requested {
		capabilities[index] = string(capability)
	}
	created, err := client.CreatePairingInvitation(context.Background(), api.InvitationRequest{
		TTLSeconds: int(ttl.Seconds()), Capabilities: capabilities,
	})
	if err != nil {
		exitf("create pairing invitation: %v", err)
	}
	fmt.Printf("syncgate pairing invitation expires_at=%s\n", created.ExpiresAt)
	fmt.Printf("fingerprint=%s\n", created.Fingerprint)
	fmt.Printf("code=%s\n", created.OneTimeCode)
	fmt.Printf("invite=%s\n", created.Invitation)
}

func runPairInspect(args []string) {
	flags := flag.NewFlagSet("pair-inspect", flag.ExitOnError)
	configPath := flags.String("config", "config.example.json", "path to syncgate JSON config")
	encoded := flags.String("invite", "", "encoded pairing invitation")
	_ = flags.Parse(args)
	if strings.TrimSpace(*encoded) == "" {
		exitf("--invite is required")
	}
	client := openAdminClient(*configPath)
	defer client.Close()
	invite, err := client.InspectPairingInvitation(context.Background(), *encoded)
	if err != nil {
		exitf("inspect pairing invitation: %v", err)
	}
	fmt.Printf("syncgate pairing peer device=%s name=%q\n", invite.DeviceID, invite.DeviceName)
	fmt.Printf("fingerprint=%s\n", invite.Fingerprint)
	fmt.Printf("expires_at=%s\n", invite.ExpiresAt)
	fmt.Printf("requested_capabilities=%s\n", strings.Join(invite.Capabilities, ","))
}

func runPairAccept(args []string) {
	flags := flag.NewFlagSet("pair-accept", flag.ExitOnError)
	configPath := flags.String("config", "config.example.json", "path to syncgate JSON config")
	encoded := flags.String("invite", "", "encoded pairing invitation")
	fingerprint := flags.String("fingerprint", "", "independently confirmed peer fingerprint")
	code := flags.String("code", "", "independently confirmed one-time code")
	lanOnly := flags.Bool("lan-only", true, "restrict all grants to LAN sessions")
	var grantValues repeatedFlag
	flags.Var(&grantValues, "grant", "explicit SHARE=capability,capability grant; repeat per share")
	_ = flags.Parse(args)
	if strings.TrimSpace(*encoded) == "" || strings.TrimSpace(*fingerprint) == "" || strings.TrimSpace(*code) == "" {
		exitf("--invite, --fingerprint, and --code are required")
	}
	grants := make([]pairing.Grant, 0, len(grantValues))
	for _, value := range grantValues {
		grant, err := parsePairingGrant(value, *lanOnly)
		if err != nil {
			exitf("invalid --grant %q: %v", value, err)
		}
		grants = append(grants, grant)
	}

	requestedGrants := make([]api.PairingGrantRequest, len(grants))
	for index, grant := range grants {
		capabilities := make([]string, len(grant.Capabilities))
		for capabilityIndex, capability := range grant.Capabilities {
			capabilities[capabilityIndex] = string(capability)
		}
		lanOnlyValue := grant.LANOnly
		requestedGrants[index] = api.PairingGrantRequest{ShareID: string(grant.ShareID), Capabilities: capabilities, LANOnly: &lanOnlyValue}
	}
	client := openAdminClient(*configPath)
	defer client.Close()
	result, err := client.AcceptPairingInvitation(context.Background(), api.AcceptanceRequest{
		Invitation: *encoded, ExpectedFingerprint: *fingerprint, OneTimeCode: *code, Grants: requestedGrants,
	})
	if err != nil {
		exitf("accept pairing invitation: %v", err)
	}
	state := "accepted"
	if result.AlreadyAccepted {
		state = "already_accepted"
	}
	fmt.Printf("syncgate pairing device=%s state=%s explicit_grants=%d\n", result.DeviceID, state, len(grants))
}

func runPairRevoke(args []string) {
	flags := flag.NewFlagSet("pair-revoke", flag.ExitOnError)
	configPath := flags.String("config", "config.example.json", "path to syncgate JSON config")
	deviceID := flags.String("device", "", "paired device ID to revoke")
	_ = flags.Parse(args)
	if strings.TrimSpace(*deviceID) == "" {
		exitf("--device is required")
	}

	client := openAdminClient(*configPath)
	defer client.Close()
	result, err := client.RevokePairingDevice(context.Background(), core.DeviceID(strings.TrimSpace(*deviceID)))
	if err != nil {
		exitf("revoke pairing: %v", err)
	}
	state := "revoked"
	if result.AlreadyRevoked {
		state = "already_revoked"
	}
	fmt.Printf("syncgate pairing device=%s state=%s\n", strings.TrimSpace(*deviceID), state)
}

func openPairingDaemon(configPath string) *daemon.Daemon {
	cfg, err := config.LoadFile(context.Background(), configPath)
	if err != nil {
		exitf("%v", err)
	}
	localDaemon, err := daemon.Bootstrap(context.Background(), cfg, daemon.Options{})
	if err != nil {
		exitf("initialize local pairing state: %v", err)
	}
	return localDaemon
}

type repeatedFlag []string

func (values *repeatedFlag) String() string { return strings.Join(*values, ";") }
func (values *repeatedFlag) Set(value string) error {
	*values = append(*values, value)
	return nil
}

func parsePairingGrant(value string, lanOnly bool) (pairing.Grant, error) {
	shareValue, capabilityValue, ok := strings.Cut(value, "=")
	shareID := core.ShareID(strings.TrimSpace(shareValue))
	if !ok || shareID == "" {
		return pairing.Grant{}, errors.New("expected SHARE=capability,capability")
	}
	capabilities, err := parsePairingCapabilities(capabilityValue)
	if err != nil {
		return pairing.Grant{}, err
	}
	if len(capabilities) == 0 {
		return pairing.Grant{}, errors.New("at least one capability is required")
	}
	return pairing.Grant{ShareID: shareID, Capabilities: capabilities, LANOnly: lanOnly}, nil
}

func parsePairingCapabilities(value string) ([]core.Capability, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, nil
	}
	seen := make(map[core.Capability]bool)
	capabilities := make([]core.Capability, 0)
	for _, item := range strings.Split(value, ",") {
		capability := core.Capability(strings.TrimSpace(item))
		if !core.IsShareCapability(capability) {
			return nil, fmt.Errorf("unsupported capability %q", capability)
		}
		if seen[capability] {
			return nil, fmt.Errorf("duplicate capability %q", capability)
		}
		seen[capability] = true
		capabilities = append(capabilities, capability)
	}
	sort.Slice(capabilities, func(i, j int) bool { return capabilities[i] < capabilities[j] })
	return capabilities, nil
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

func runDaemonStatus(args []string) {
	flags := flag.NewFlagSet("daemon-status", flag.ExitOnError)
	configPath := flags.String("config", "config.example.json", "path to syncgate JSON config")
	_ = flags.Parse(args)

	client := openAdminClient(*configPath)
	defer client.Close()
	status, err := client.Status(context.Background())
	if err != nil {
		exitf("read daemon status: %v", err)
	}
	fmt.Printf("syncgate daemon status=%s api=%s device=%s shares=%d peer_executor_enabled=%t queue_pending=%d queue_running=%d queue_paused=%d queue_failed=%d\n",
		status.Status, status.APIVersion, status.DeviceID, status.ActiveShareCount, status.PeerExecutorEnabled,
		status.Queue.Pending, status.Queue.Running, status.Queue.Paused, status.Queue.Failed)
	diagnostics, err := client.Diagnostics(context.Background(), 50)
	if err != nil {
		exitf("read daemon diagnostics: %v", err)
	}
	printDiagnostics(os.Stdout, syncengine.DiagnosticReport{
		GeneratedAt: diagnostics.GeneratedAt, RecentScans: diagnostics.RecentScans,
		Work: diagnostics.Work, IgnoredPaths: diagnostics.IgnoredPaths,
	}, 50)
}

func runScan(args []string) {
	flags := flag.NewFlagSet("scan", flag.ExitOnError)
	configPath := flags.String("config", "config.example.json", "path to syncgate JSON config")
	shareID := flags.String("share", "", "source share ID to scan")
	_ = flags.Parse(args)
	if *shareID == "" {
		exitf("--share is required")
	}

	client := openAdminClient(*configPath)
	defer client.Close()
	accepted, err := client.RequestScan(context.Background(), core.ShareID(*shareID))
	if err != nil {
		exitf("scan share %s: %v", *shareID, err)
	}
	fmt.Printf("syncgate scan share=%s accepted=%t\n", accepted.ShareID, accepted.Accepted)
}

func runProjectMigratePreflight(args []string) {
	flags := flag.NewFlagSet("project-migrate-preflight", flag.ExitOnError)
	configPath := flags.String("config", "config.example.json", "path to syncgate JSON config")
	shareID := flags.String("share", "", "configured source share ID")
	projectID := flags.String("project", "", "new Agent Project ID")
	name := flags.String("name", "", "Agent Project display name")
	_ = flags.Parse(args)
	input := migrationCLIInput(*shareID, *projectID, *name)
	client := openAdminClient(*configPath)
	defer client.Close()
	result, err := client.PreflightProjectMigration(context.Background(), input)
	if err != nil {
		exitf("preflight project migration: %v", err)
	}
	if err := json.NewEncoder(os.Stdout).Encode(result); err != nil {
		exitf("print project migration preflight: %v", err)
	}
}

func runProjectMigrateApply(args []string) {
	flags := flag.NewFlagSet("project-migrate-apply", flag.ExitOnError)
	configPath := flags.String("config", "config.example.json", "path to syncgate JSON config")
	shareID := flags.String("share", "", "configured source share ID")
	projectID := flags.String("project", "", "Agent Project ID from preflight")
	name := flags.String("name", "", "Agent Project display name from preflight")
	confirmation := flags.String("confirmation", "", "exact confirmation digest returned by preflight")
	_ = flags.Parse(args)
	input := api.ProjectMigrationApplyInput{ProjectMigrationInput: migrationCLIInput(*shareID, *projectID, *name), Confirmation: strings.TrimSpace(*confirmation)}
	client := openAdminClient(*configPath)
	defer client.Close()
	result, err := client.ApplyProjectMigration(context.Background(), input)
	if err != nil {
		exitf("apply project migration: %v", err)
	}
	if err := json.NewEncoder(os.Stdout).Encode(result); err != nil {
		exitf("print project migration result: %v", err)
	}
}

func migrationCLIInput(shareID, projectID, name string) api.ProjectMigrationInput {
	input := api.ProjectMigrationInput{ShareID: strings.TrimSpace(shareID), ProjectID: strings.TrimSpace(projectID), Name: strings.TrimSpace(name)}
	if input.ShareID == "" || input.ProjectID == "" || input.Name == "" {
		exitf("--share, --project, and --name are required")
	}
	return input
}

func runJobControl(args []string, control syncengine.OneWayJobControl) {
	flags := flag.NewFlagSet("job-"+string(control), flag.ExitOnError)
	configPath := flags.String("config", "config.example.json", "path to syncgate JSON config")
	jobID := flags.String("job", "", "one-way job ID")
	_ = flags.Parse(args)
	if *jobID == "" {
		exitf("--job is required")
	}

	client := openAdminClient(*configPath)
	defer client.Close()
	job, err := client.ControlJob(context.Background(), *jobID, api.JobActionName(control))
	if err != nil {
		exitf("%v", err)
	}
	fmt.Printf("syncgate job=%s state=%s retries=%d\n", job.ID, job.State, job.RetryCount)
}

func openAdminClient(configPath string) *api.Client {
	client, err := newAdminClient(configPath)
	if err != nil {
		exitf("%v", err)
	}
	return client
}

func newAdminClient(configPath string) (*api.Client, error) {
	cfg, err := config.LoadFile(context.Background(), configPath)
	if err != nil {
		return nil, err
	}
	credentialStore, err := api.NewAdminCredentialStore(api.AdminCredentialStoreOptions{
		DataDir: cfg.DataDir, RuntimeMode: cfg.RuntimeMode,
		AllowInsecureDevelopmentFile: cfg.Identity.AllowInsecureDevelopmentFile,
	})
	if err != nil {
		return nil, fmt.Errorf("configure local administration credential: %w", err)
	}
	credential, err := api.LoadAdminCredential(credentialStore)
	if err != nil {
		return nil, fmt.Errorf("load local administration credential: %w", err)
	}
	client, err := api.NewClient(api.ClientOptions{
		Address: net.JoinHostPort(cfg.LocalAPI.Host, fmt.Sprint(cfg.LocalAPI.Port)), Credential: credential,
	})
	for index := range credential {
		credential[index] = 0
	}
	if err != nil {
		return nil, fmt.Errorf("configure local administration client: %w", err)
	}
	return client, nil
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
	configPath := flags.String("config", "config.example.json", "path to syncgate JSON config")
	listen := flags.String("listen", "127.0.0.1:47821", "manual TCP/TLS listen address")
	shareRoot := flags.String("share-root", "", "destination share root")
	_ = flags.Parse(args)
	if *shareRoot == "" {
		exitf("--share-root is required")
	}

	localDaemon := openPairingDaemon(*configPath)
	defer localDaemon.Close()
	tlsConfig, err := tcptls.IdentityTLSConfig(tcptls.IdentityTLSConfigOptions{
		Identity:     localDaemon.Identity,
		PeerVerifier: pairing.TrustedPeerVerifier{Devices: localDaemon.Store.Devices()},
		Server:       true,
	})
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
	fmt.Printf("syncgate receiving on %s device=%s\n", server.Address, localDaemon.Identity.DeviceID)

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
	configPath := flags.String("config", "config.example.json", "path to syncgate JSON config")
	addr := flags.String("addr", "127.0.0.1:47821", "manual TCP/TLS receiver address")
	source := flags.String("file", "", "source file to send")
	relativePath := flags.String("relative-path", "", "destination relative path inside receiver share")
	peerDeviceID := flags.String("peer", "", "expected paired receiver device ID")
	chunkSize := flags.Int64("chunk-size", transfer.DefaultChunkSize, "fixed transfer chunk size")
	_ = flags.Parse(args)
	if *source == "" {
		exitf("--file is required")
	}
	if *relativePath == "" {
		exitf("--relative-path is required")
	}
	if strings.TrimSpace(*peerDeviceID) == "" {
		exitf("--peer is required")
	}

	localDaemon := openPairingDaemon(*configPath)
	defer localDaemon.Close()
	tlsConfig, err := tcptls.IdentityTLSConfig(tcptls.IdentityTLSConfigOptions{
		Identity:     localDaemon.Identity,
		PeerVerifier: pairing.TrustedPeerVerifier{Devices: localDaemon.Store.Devices()},
	})
	if err != nil {
		exitf("%v", err)
	}
	client := tcptls.New(*addr, tlsConfig)
	session, err := client.Connect(context.Background(), core.DeviceID(strings.TrimSpace(*peerDeviceID)))
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
