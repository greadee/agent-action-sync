package main

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"syncgate/internal/config"
	"syncgate/internal/desktop"
	"syncgate/internal/identity"
	"syncgate/internal/resultintake"
	"syncgate/internal/taskspec"
)

const deleteProviderCredentialConfirmation = "DELETE PROVIDER CREDENTIAL"

func runNodeSettingsShow(args []string) {
	flags := flag.NewFlagSet("node-settings-show", flag.ExitOnError)
	root := flags.String("root", "", "optional explicit desktop node root")
	_ = flags.Parse(args)
	view, err := desktop.SettingsManager{Base: resolveDesktopRoots(*root)}.View(context.Background())
	if err != nil {
		exitf("read desktop settings: %v", err)
	}
	printJSON(view)
}

func runNodeSettingsStage(args []string) {
	flags := flag.NewFlagSet("node-settings-stage", flag.ExitOnError)
	root := flags.String("root", "", "optional explicit desktop node root")
	deviceName := flags.String("device-name", "", "local device display name")
	apiHost := flags.String("api-host", "", "loopback local API host")
	apiPort := flags.Int("api-port", 0, "loopback local API port")
	dataDir := flags.String("data-dir", "", "absolute SQLite/control-state root")
	logDir := flags.String("log-dir", "", "absolute log root")
	cacheDir := flags.String("cache-dir", "", "absolute runtime cache root")
	worktreeRoot := flags.String("worktree-root", "", "absolute Git worktree root")
	_ = flags.Parse(args)
	mutation := desktop.SettingsMutation{}
	flags.Visit(func(item *flag.Flag) {
		switch item.Name {
		case "device-name":
			mutation.DeviceName = deviceName
		case "api-host":
			mutation.APIHost = apiHost
		case "api-port":
			mutation.APIPort = apiPort
		case "data-dir":
			mutation.DataDir = dataDir
		case "log-dir":
			mutation.LogDir = logDir
		case "cache-dir":
			mutation.RuntimeCacheDir = cacheDir
		case "worktree-root":
			mutation.WorktreeRoot = worktreeRoot
		}
	})
	staged, err := (desktop.SettingsManager{Base: resolveDesktopRoots(*root)}).Stage(context.Background(), mutation)
	if err != nil {
		exitf("stage desktop settings: %v", err)
	}
	printJSON(staged)
}

func runNodeShareStage(args []string) {
	flags := flag.NewFlagSet("node-share-stage", flag.ExitOnError)
	root := flags.String("root", "", "optional explicit desktop node root")
	id := flags.String("id", "", "stable share ID")
	name := flags.String("name", "", "share display name")
	shareRoot := flags.String("share-root", "", "absolute registered share root")
	mode := flags.String("mode", "", "share mode")
	var ignores repeatedFlag
	flags.Var(&ignores, "ignore", "relative ignore pattern; repeat as needed")
	_ = flags.Parse(args)
	if *id == "" || *name == "" || *shareRoot == "" || *mode == "" {
		exitf("--id, --name, --share-root, and --mode are required")
	}
	staged, err := (desktop.SettingsManager{Base: resolveDesktopRoots(*root)}).StageShare(context.Background(), config.ShareConfig{
		ID: *id, Name: *name, RootPath: *shareRoot, Mode: *mode, IgnorePatterns: append([]string(nil), ignores...),
	})
	if err != nil {
		exitf("stage share registration: %v", err)
	}
	printJSON(staged)
}

func runNodeSettingsApply(args []string) {
	flags := flag.NewFlagSet("node-settings-apply", flag.ExitOnError)
	root := flags.String("root", "", "optional explicit desktop node root")
	confirmation := flags.String("confirmation", "", "exact candidate digest returned by staging")
	_ = flags.Parse(args)
	if *confirmation == "" {
		exitf("--confirmation is required")
	}
	view, err := (desktop.SettingsManager{Base: resolveDesktopRoots(*root)}).Apply(context.Background(), *confirmation)
	if err != nil {
		exitf("apply desktop settings: %v", err)
	}
	printJSON(view)
}

func runNodeSettingsDiscard(args []string) {
	flags := flag.NewFlagSet("node-settings-discard", flag.ExitOnError)
	root := flags.String("root", "", "optional explicit desktop node root")
	_ = flags.Parse(args)
	if err := (desktop.SettingsManager{Base: resolveDesktopRoots(*root)}).Discard(); err != nil {
		exitf("discard desktop settings: %v", err)
	}
	fmt.Println("syncgate pending desktop settings discarded")
}

func runNodeIdentityStatus(args []string) {
	flags := flag.NewFlagSet("node-identity-status", flag.ExitOnError)
	root := flags.String("root", "", "optional explicit desktop node root")
	_ = flags.Parse(args)
	cfg, roots, err := (desktop.SettingsManager{Base: resolveDesktopRoots(*root)}).Active(context.Background())
	if err != nil {
		exitf("read desktop settings: %v", err)
	}
	status, err := desktop.ReadIdentityStatus(roots.DataDir, cfg.Identity.Store)
	if err != nil {
		exitf("read identity status: %v", err)
	}
	printJSON(status)
}

func runNodeCredentialSet(args []string) {
	flags := flag.NewFlagSet("node-credential-set", flag.ExitOnError)
	root := flags.String("root", "", "optional explicit desktop node root")
	provider := flags.String("provider", "", "provider identifier")
	fromStdin := flags.Bool("from-stdin", false, "read the credential from bounded stdin")
	_ = flags.Parse(args)
	if *provider == "" || !*fromStdin {
		exitf("--provider and --from-stdin are required; credentials are never accepted as arguments")
	}
	secretBuffer, err := io.ReadAll(io.LimitReader(os.Stdin, identity.MaxOSSecretBytes+1))
	if err != nil {
		exitf("read provider credential: %v", err)
	}
	defer clearCLISecret(secretBuffer)
	secret := bytes.TrimRight(secretBuffer, "\r\n")
	if len(secret) < 1 || len(secret) > identity.MaxOSSecretBytes {
		exitf("provider credential must be between 1 and %d bytes", identity.MaxOSSecretBytes)
	}
	service := providerCredentials(resolveDesktopRoots(*root))
	status, err := service.Set(*provider, secret)
	if err != nil {
		exitf("store provider credential: %v", err)
	}
	printJSON(status)
}

func runNodeCredentialStatus(args []string) {
	flags := flag.NewFlagSet("node-credential-status", flag.ExitOnError)
	root := flags.String("root", "", "optional explicit desktop node root")
	provider := flags.String("provider", "", "provider identifier")
	_ = flags.Parse(args)
	if *provider == "" {
		exitf("--provider is required")
	}
	status, err := providerCredentials(resolveDesktopRoots(*root)).Status(*provider)
	if err != nil {
		exitf("read provider credential status: %v", err)
	}
	printJSON(status)
}

func runNodeCredentialDelete(args []string) {
	flags := flag.NewFlagSet("node-credential-delete", flag.ExitOnError)
	root := flags.String("root", "", "optional explicit desktop node root")
	provider := flags.String("provider", "", "provider identifier")
	confirmation := flags.String("confirm", "", "exact deletion confirmation phrase")
	_ = flags.Parse(args)
	if *provider == "" || *confirmation != deleteProviderCredentialConfirmation {
		exitf("--provider and --confirm %q are required", deleteProviderCredentialConfirmation)
	}
	status, err := providerCredentials(resolveDesktopRoots(*root)).Delete(*provider)
	if err != nil {
		exitf("delete provider credential: %v", err)
	}
	printJSON(status)
}

func runNodeDisposableMark(args []string) {
	flags := flag.NewFlagSet("node-disposable-mark", flag.ExitOnError)
	root := flags.String("root", "", "optional explicit desktop node root")
	project := flags.String("project", "", "absolute disposable Git project root")
	confirmation := flags.String("confirm", "", "exact disposable marker confirmation phrase")
	_ = flags.Parse(args)
	marker, err := executionManager(resolveDesktopRoots(*root)).MarkDisposable(context.Background(), *project, *confirmation)
	if err != nil {
		exitf("mark disposable project: %v", err)
	}
	printJSON(marker)
}

func runNodeExecutionPreflight(args []string) {
	flags := flag.NewFlagSet("node-execution-preflight", flag.ExitOnError)
	root := flags.String("root", "", "optional explicit desktop node root")
	provider := flags.String("provider", "", "provider identifier")
	model := flags.String("model", "gpt-5.6-sol", "bounded model identifier")
	runtimeExecutable := flags.String("runtime", "", "absolute supervised runtime executable")
	project := flags.String("project", "", "absolute disposable Git project root")
	confirmation := flags.String("confirm", "", "exact disposable-project confirmation phrase")
	_ = flags.Parse(args)
	result, err := executionManager(resolveDesktopRoots(*root)).Preflight(context.Background(), *provider, *model, *runtimeExecutable, *project, *confirmation)
	if err != nil {
		exitf("preflight local execution: %v", err)
	}
	printJSON(result)
}

func runNodeExecutionEnable(args []string) {
	flags := flag.NewFlagSet("node-execution-enable", flag.ExitOnError)
	root := flags.String("root", "", "optional explicit desktop node root")
	receipt := flags.String("receipt", "", "short-lived preflight receipt")
	confirmation := flags.String("confirm", "", "exact execution enablement phrase")
	_ = flags.Parse(args)
	state, err := executionManager(resolveDesktopRoots(*root)).Enable(context.Background(), *receipt, *confirmation)
	if err != nil {
		exitf("enable local execution: %v", err)
	}
	printJSON(state)
}

func runNodeExecutionDisable(args []string) {
	flags := flag.NewFlagSet("node-execution-disable", flag.ExitOnError)
	root := flags.String("root", "", "optional explicit desktop node root")
	confirmation := flags.String("confirm", "", "exact execution disable phrase")
	_ = flags.Parse(args)
	state, err := executionManager(resolveDesktopRoots(*root)).Disable(context.Background(), *confirmation)
	if err != nil {
		exitf("disable local execution: %v", err)
	}
	printJSON(state)
}

func runNodeExecutionStatus(args []string) {
	flags := flag.NewFlagSet("node-execution-status", flag.ExitOnError)
	root := flags.String("root", "", "optional explicit desktop node root")
	_ = flags.Parse(args)
	state, err := executionManager(resolveDesktopRoots(*root)).State(context.Background())
	if err != nil {
		exitf("read local execution status: %v", err)
	}
	printJSON(state)
}

func runNodeDiagnosticsExport(args []string) {
	flags := flag.NewFlagSet("node-diagnostics-export", flag.ExitOnError)
	root := flags.String("root", "", "optional explicit desktop node root")
	output := flags.String("file", "", "absolute sanitized diagnostics JSON path")
	_ = flags.Parse(args)
	base := resolveDesktopRoots(*root)
	report, err := (desktop.DiagnosticsExporter{Base: base, Credentials: providerCredentials(base)}).Export(context.Background(), *output)
	if err != nil {
		exitf("export sanitized diagnostics: %v", err)
	}
	printJSON(report)
}

func runNodeResources(args []string) {
	flags := flag.NewFlagSet("node-resources", flag.ExitOnError)
	root := flags.String("root", "", "optional explicit desktop node root")
	_ = flags.Parse(args)
	base := resolveDesktopRoots(*root)
	cfg, roots, err := (desktop.SettingsManager{Base: base}).Active(context.Background())
	if err != nil {
		exitf("read desktop settings: %v", err)
	}
	ceiling := cfg.Node.Execution.MaxConcurrent
	if ceiling == 0 {
		ceiling = 1
	}
	resources, err := desktop.ObserveMachineResources(roots.WorktreeRoot, ceiling, time.Now().UTC())
	if err != nil {
		exitf("observe local machine resources: %v", err)
	}
	printJSON(resources)
}

func runNodeResultImport(args []string) {
	flags := flag.NewFlagSet("node-result-import", flag.ExitOnError)
	root := flags.String("root", "", "optional explicit desktop node root")
	fromStdin := flags.Bool("from-stdin", false, "read a bounded result envelope from stdin")
	_ = flags.Parse(args)
	if !*fromStdin {
		exitf("--from-stdin is required")
	}
	base := resolveDesktopRoots(*root)
	cfg, roots, err := (desktop.SettingsManager{Base: base}).Active(context.Background())
	if err != nil {
		exitf("read desktop settings: %v", err)
	}
	if !cfg.Node.Execution.Enabled {
		exitf("local execution is disabled")
	}
	raw, err := io.ReadAll(io.LimitReader(os.Stdin, resultintake.MaxEnvelopeBytes+1))
	if err != nil || len(raw) > resultintake.MaxEnvelopeBytes {
		exitf("read bounded result envelope: input exceeds the limit")
	}
	envelope, err := (desktop.LocalResultStore{Root: filepath.Join(roots.DataDir, "orchestration", "results")}).PutEnvelope(context.Background(), raw)
	for index := range raw {
		raw[index] = 0
	}
	if err != nil {
		exitf("import result envelope: %v", err)
	}
	printJSON(struct {
		ResultID string `json:"result_id"`
		Digest   string `json:"digest"`
	}{ResultID: envelope.ResultID, Digest: envelope.Digest})
}

func runNodeTaskSpecificationImport(args []string) {
	flags := flag.NewFlagSet("node-task-spec-import", flag.ExitOnError)
	root := flags.String("root", "", "optional explicit desktop node root")
	fromStdin := flags.Bool("from-stdin", false, "read a bounded task specification from stdin")
	_ = flags.Parse(args)
	if !*fromStdin {
		exitf("--from-stdin is required")
	}
	base := resolveDesktopRoots(*root)
	_, roots, err := (desktop.SettingsManager{Base: base}).Active(context.Background())
	if err != nil {
		exitf("read desktop settings: %v", err)
	}
	raw, err := io.ReadAll(io.LimitReader(os.Stdin, taskspec.MaxBytes+1))
	if err != nil || len(raw) > taskspec.MaxBytes {
		exitf("read bounded task specification: input exceeds the limit")
	}
	stored, err := (taskspec.FileStore{Root: filepath.Join(roots.DataDir, "orchestration", "task-specifications")}).Put(context.Background(), raw)
	for index := range raw {
		raw[index] = 0
	}
	if err != nil {
		exitf("import task specification: %v", err)
	}
	printJSON(struct {
		SpecificationID string `json:"specification_id"`
		Digest          string `json:"digest"`
		ProjectID       string `json:"project_id"`
		TaskID          string `json:"task_id"`
		AlreadyPresent  bool   `json:"already_present"`
	}{
		SpecificationID: stored.Specification.SpecificationID, Digest: stored.Digest,
		ProjectID: stored.Specification.ProjectID, TaskID: stored.Specification.TaskID, AlreadyPresent: stored.AlreadyPresent,
	})
}

func providerCredentials(base desktop.Roots) desktop.ProviderCredentials {
	return desktop.ProviderCredentials{ScopeRoot: base.ConfigDir}
}

func executionManager(base desktop.Roots) desktop.ExecutionManager {
	return desktop.ExecutionManager{Base: base, Credentials: providerCredentials(base)}
}

func clearCLISecret(secret []byte) {
	for index := range secret {
		secret[index] = 0
	}
}
