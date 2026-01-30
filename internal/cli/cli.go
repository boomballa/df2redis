package cli

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"df2redis/internal/checker"
	"df2redis/internal/config"
	"df2redis/internal/logger"
	"df2redis/internal/replica"
	"df2redis/internal/state"
	"df2redis/internal/web"
)

// Execute dispatches CLI subcommands.
func Execute(args []string) int {
	log.SetFlags(log.LstdFlags | log.Lmsgprefix)
	log.SetPrefix("[df2redis] ")

	// Ignore SIGHUP to allow running in background without nohup
	// When SSH session disconnects, the shell sends SIGHUP to all child processes.
	// By ignoring SIGHUP, df2redis can continue running after session disconnect.
	// This enables simple background execution: ./df2redis replicate &
	signal.Ignore(syscall.SIGHUP)

	// Ignore SIGPIPE to prevent exit when writing to closed stdout/stderr (e.g. SSH disconnect)
	// Go runtime defaults to exit on SIGPIPE. Ignoring it allows Write to return EPIPE error instead,
	// which our logger handles gracefully.
	signal.Ignore(syscall.SIGPIPE)

	if len(args) == 0 {
		printUsage()
		return 1
	}

	switch args[0] {
	case "prepare":
		return runPrepare(args[1:])
	case "migrate":
		return runMigrate(args[1:])
	case "replicate":
		return runReplicate(args[1:])
	case "check":
		return runCheck(args[1:])
	case "status":
		return runStatus(args[1:])
	case "rollback":
		return runRollback(args[1:])
	case "dashboard":
		return runDashboard(args[1:])

	case "help", "-h", "--help":
		printUsage()
		return 0
	case "version", "--version", "-v":
		fmt.Println("df2redis 0.1.0-dev")
		return 0
	default:
		log.Printf("Unknown subcommand: %s", args[0])
		printUsage()
		return 1
	}
}

func runPrepare(args []string) int {
	cfg, err := loadConfigFromArgs("prepare", args)
	if err != nil {
		return errorToExitCode(err)
	}
	if err := cfg.Validate(); err != nil {
		return errorToExitCode(err)
	}
	if err := cfg.EnsureStateDir(); err != nil {
		log.Printf("Failed to create state directory: %v", err)
		return 1
	}
	log.Printf("🛠️ Preparation complete:\n  📂 stateDir  : %s\n  📝 statusFile: %s",
		cfg.ResolveStateDir(), cfg.StatusFilePath())
	return 0
}

func runMigrate(args []string) int {
	fs := flag.NewFlagSet("migrate", flag.ContinueOnError)
	fs.SetOutput(os.Stdout)
	var configPath string
	var dryRun bool
	var showPort int
	var showAddr string
	var verify bool // New flag

	fs.StringVar(&configPath, "config", "", "Configuration file path (YAML)")
	fs.StringVar(&configPath, "c", "", "Configuration file path (YAML)")
	fs.BoolVar(&dryRun, "dry-run", false, "Validate configuration only without running migration")
	fs.IntVar(&showPort, "show", 0, "Start embedded dashboard on the given port (e.g. --show 8080)")
	fs.StringVar(&showAddr, "show-addr", "", "Start embedded dashboard on the given address (e.g. --show-addr 0.0.0.0:8080)")
	fs.BoolVar(&verify, "verify", false, "Run data consistency check after migration (smart mode)")

	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		log.Printf("Failed to parse arguments: %v", err)
		return 1
	}
	if configPath == "" {
		log.Println("The --config flag is required")
		fs.Usage()
		return 2
	}

	cfg, err := config.Load(configPath)
	if err != nil {
		log.Printf("Failed to load config: %v", err)
		return 2
	}
	if err := cfg.Validate(); err != nil {
		log.Printf("Config validation failed: %v", err)
		return 2
	}
	log.Printf("✅ Config loaded:\n%s", cfg.PrettySummary())

	if dryRun {
		log.Println("🚧 Dry-run mode: configuration only; no migration will run.")
		return 0
	}

	if err := cfg.EnsureStateDir(); err != nil {
		log.Printf("Failed to create state directory: %v", err)
		return 1
	}

	// Initialize logging for migrate command
	if err := initLogger(cfg, "migrate"); err != nil {
		log.Printf("Failed to initialize logging: %v", err)
		return 1
	}
	defer logger.Close()

	store := state.NewStore(cfg.StatusFilePath())
	_ = store.SetPipelineStatus("starting", "Preparing to run migration")

	var dashboardAddr string
	if showAddr != "" {
		if !strings.Contains(showAddr, ":") {
			if showPort > 0 {
				showAddr = fmt.Sprintf("%s:%d", showAddr, showPort)
			} else {
				log.Printf("show-addr must include a port, e.g. 0.0.0.0:8080")
				return 2
			}
		}
		if _, _, err := net.SplitHostPort(showAddr); err != nil {
			log.Printf("Invalid show-addr format: %v", err)
			return 2
		}
		dashboardAddr = showAddr
	} else if showPort > 0 {
		dashboardAddr = fmt.Sprintf(":%d", showPort)
	}

	if dashboardAddr != "" && !dryRun {
		go func() {
			server, err := web.New(web.Options{
				Addr:  dashboardAddr,
				Cfg:   cfg,
				Store: store,
			})
			if err != nil {
				logger.Error("Failed to initialize dashboard: %v", err)
				return
			}
			logger.Console("\n━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n📺 Auto dashboard ready\n   🔊 Listen : %s\n   🌐 Visit : %s\n   ⌨️ Hint  : press Ctrl+C to stop the dashboard service\n━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━", dashboardAddr, formatDashboardURL(dashboardAddr))
			if err := server.Start(nil); err != nil {
				logger.Warn("dashboard stopped: %v", err)
			}
		}()
	}

	// Migration Mode: Snapshot Only = True
	cfg.Migrate.SnapshotOnly = true

	logger.Console("🚀 df2redis migration starting (Snapshot Only)")
	logger.Console("📋 Config dir: %s", cfg.ConfigDir())
	logger.Console("📂 Log dir: %s", cfg.Log.Dir)
	logger.Console("🎯 Target: %s", cfg.Target.Addr)

	// Build replicator
	replicator := replica.NewReplicator(cfg)
	replicator.AttachStateStore(store)

	// Configure signal handling
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	// Start replicator
	errCh := make(chan error, 1)
	go func() {
		if err := replicator.Start(); err != nil {
			errCh <- err
		} else {
			errCh <- nil // Success
		}
	}()

	// Wait for completion or signal
	select {
	case err := <-errCh:
		if err != nil {
			logger.Error("❌ Migration failed: %v", err)
			return 1
		}
		logger.Console("\n✅ Migration completed successfully")

		// ---------------------
		// Post-Migration Verify
		// ---------------------
		if verify {
			logger.Console("\n🔍 Starting post-migration verification (smart mode)...")

			// Create checker config
			checkCfg := checker.Config{
				SourceAddr:      cfg.Source.Addr,
				SourcePassword:  cfg.Source.Password,
				TargetAddr:      cfg.Target.Addr,
				TargetPassword:  cfg.Target.Password,
				Mode:            checker.ModeSmartBigKey, // Default to smart mode for verify flag
				QPS:             5000,
				Parallel:        4,
				ResultDir:       "check-results",
				BatchSize:       1000,
				Timeout:         3600,
				BigKeyThreshold: 5000,
				TaskName:        "verify-after-migrate",
			}

			c := checker.NewChecker(checkCfg)
			progressCh := make(chan checker.Progress, 100)

			// Simple progress reporter for CLI
			go func() {
				for p := range progressCh {
					if p.TotalKeys > 0 && p.TotalKeys%1000 == 0 {
						fmt.Printf("\rChecked: %d keys | Inconsistent: %d | Missing: %d", p.CheckedKeys, p.InconsistentKeys, p.MissingKeys)
					}
				}
				fmt.Println()
			}()

			ctx := context.Background()
			result, err := c.Run(ctx, progressCh)
			close(progressCh)

			if err != nil {
				logger.Error("❌ Verification failed: %v", err)
				return 1
			}

			c.PrintResult(result)

			if result.InconsistentKeys > 0 || result.MissingKeys > 0 {
				logger.Error("❌ Verification found inconsistencies! See %s", result.ResultFile)
				return 1
			}
			logger.Console("✅ Verification passed! Source and Target are consistent.")
		}

		return 0
	case sig := <-sigCh:
		logger.Console("\n📡 Signal %v received, shutting down...", sig)
		replicator.Stop()
		return 0
	}
}

func runStatus(args []string) int {
	cfg, err := loadConfigFromArgs("status", args)
	if err != nil {
		return errorToExitCode(err)
	}
	store := state.NewStore(cfg.StatusFilePath())
	snap, err := store.Load()
	if err != nil {
		log.Printf("Failed to read status file: %v", err)
		return 1
	}
	log.Printf("📊 pipeline=%s updatedAt=%s", snap.PipelineStatus, snap.UpdatedAt.Format(time.RFC3339))
	for name, st := range snap.Stages {
		log.Printf("stage %-12s status=%s updated=%s msg=%s", name, st.Status, st.UpdatedAt.Format(time.RFC3339), st.Message)
	}
	if len(snap.Metrics) > 0 {
		log.Println("metrics:")
		for k, v := range snap.Metrics {
			log.Printf("  📈 %s=%.4f", k, v)
		}
	}
	if len(snap.Events) > 0 {
		log.Println("events:")
		for _, ev := range snap.Events {
			log.Printf("  🗒️ [%s] %s - %s", ev.Timestamp.Format(time.RFC3339), ev.Type, ev.Message)
		}
	}
	return 0
}

func runRollback(args []string) int {
	cfg, err := loadConfigFromArgs("rollback", args)
	if err != nil {
		return errorToExitCode(err)
	}
	store := state.NewStore(cfg.StatusFilePath())
	if err := store.SetPipelineStatus("rolling_back", "Starting rollback process"); err != nil {
		log.Printf("Failed to update status: %v", err)
		return 1
	}
	if err := store.SetPipelineStatus("rolled_back", "Rollback marked; awaiting manual cutover"); err != nil {
		log.Printf("Failed to update status: %v", err)
		return 1
	}
	log.Printf("↩️ Rollback marker written, stateDir=%s", cfg.ResolveStateDir())
	return 0
}

func runDashboard(args []string) int {
	fs := flag.NewFlagSet("dashboard", flag.ContinueOnError)
	fs.SetOutput(os.Stdout)
	var (
		configPath string
		addr       string
	)
	fs.StringVar(&configPath, "config", "", "Configuration file path (YAML)")
	fs.StringVar(&configPath, "c", "", "Configuration file path (YAML)")
	fs.StringVar(&addr, "addr", "", "Dashboard listen address (defaults to dashboard.addr when empty)")

	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		log.Printf("Failed to parse arguments: %v", err)
		return 1
	}
	if configPath == "" {
		fs.Usage()
		return 2
	}

	cfg, err := config.Load(configPath)
	if err != nil {
		log.Printf("Failed to load config: %v", err)
		return 2
	}
	if addr == "" {
		addr = cfg.Dashboard.Addr
	}
	store := state.NewStore(cfg.StatusFilePath())

	server, err := web.New(web.Options{
		Addr:  addr,
		Cfg:   cfg,
		Store: store,
	})
	if err != nil {
		log.Printf("Failed to initialize dashboard: %v", err)
		return 1
	}

	log.Printf("\n━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n📺 Dashboard ready\n   🔊 Listen : %s\n   🌐 Visit : %s\n   ⌨️ Hint  : press Ctrl+C to stop the dashboard service\n━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━", addr, formatDashboardURL(addr))
	if err := server.Start(nil); err != nil {
		if strings.Contains(err.Error(), "address already in use") {
			log.Printf("dashboard failed: port %s already in use, change dashboard.addr or pass --addr", addr)
		} else {
			log.Printf("dashboard stopped: %v", err)
		}
		return 1
	}
	return 0
}

func formatDashboardURL(addr string) string {
	if addr == "" {
		return ""
	}
	clean := addr
	if strings.HasPrefix(clean, "http://") || strings.HasPrefix(clean, "https://") {
		return clean
	}
	if strings.HasPrefix(clean, ":") {
		port := strings.TrimPrefix(clean, ":")
		return fmt.Sprintf("http://127.0.0.1:%s (or http://<server-ip>:%s)", port, port)
	}
	host, port, err := net.SplitHostPort(clean)
	if err != nil {
		return "http://" + clean
	}
	switch host {
	case "", "0.0.0.0", "::", "[::]":
		return fmt.Sprintf("http://<server-ip>:%s (listening on %s:%s)", port, host, port)
	default:
		return fmt.Sprintf("http://%s:%s", host, port)
	}
}

func loadConfigFromArgs(cmd string, args []string) (*config.Config, error) {
	fs := flag.NewFlagSet(cmd, flag.ContinueOnError)
	fs.SetOutput(os.Stdout)
	var configPath string
	fs.StringVar(&configPath, "config", "", "Configuration file path (YAML)")
	fs.StringVar(&configPath, "c", "", "Configuration file path (YAML)")

	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return nil, flag.ErrHelp
		}
		return nil, fmt.Errorf("Failed to parse arguments: %w", err)
	}
	if configPath == "" {
		fs.Usage()
		return nil, fmt.Errorf("The --config flag is required")
	}

	cfg, err := config.Load(configPath)
	if err != nil {
		return nil, err
	}
	return cfg, nil
}

func errorToExitCode(err error) int {
	if err == flag.ErrHelp {
		return 0
	}
	log.Printf("Command execution failed: %v", err)
	return 1
}

func runReplicate(args []string) int {
	fs := flag.NewFlagSet("replicate", flag.ContinueOnError)
	fs.SetOutput(os.Stdout)
	var configPath string
	var dashboardAddr string
	var taskNameFlag string
	fs.StringVar(&configPath, "config", "", "Configuration file path (YAML)")
	fs.StringVar(&configPath, "c", "", "Configuration file path (YAML)")
	fs.StringVar(&dashboardAddr, "dashboard-addr", "", "Embedded dashboard listen address (empty to use config, set to empty string to disable)")
	fs.StringVar(&taskNameFlag, "task-name", "", "Task name (used for log prefix; overrides config file)")

	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		log.Printf("Failed to parse arguments: %v", err)
		return 1
	}
	if configPath == "" {
		fs.Usage()
		return 2
	}

	cfg, err := config.Load(configPath)
	if err != nil {
		return errorToExitCode(err)
	}
	if err := cfg.Validate(); err != nil {
		return errorToExitCode(err)
	}
	if taskNameFlag != "" {
		cfg.TaskName = taskNameFlag
	}
	if dashboardAddr == "" {
		dashboardAddr = cfg.Dashboard.Addr
	}
	store := state.NewStore(cfg.StatusFilePath())
	_ = store.SetPipelineStatus("starting", "Preparing to start replicator")

	// Initialize logging
	if err := initLogger(cfg, "replicate"); err != nil {
		log.Printf("Failed to initialize logging: %v", err)
		return 1
	}
	defer logger.Close()

	logger.Console("🚀 df2redis replicator starting")
	logger.Console("📋 Config dir: %s", cfg.ConfigDir())
	logger.Console("📂 Log dir: %s", cfg.Log.Dir)
	logger.Console("📝 Log level: %s", cfg.Log.Level)
	logger.Console("📄 Log file: %s", logger.GetLogFilePath())

	// Build replicator
	replicator := replica.NewReplicator(cfg)
	replicator.AttachStateStore(store)

	if dashboardAddr != "" {
		server, err := web.New(web.Options{
			Addr:  dashboardAddr,
			Cfg:   cfg,
			Store: store,
		})
		if err != nil {
			logger.Error("Failed to initialize embedded dashboard: %v", err)
			return 1
		}
		dashErr := make(chan error, 1)
		ready := make(chan string, 1)
		go func() {
			dashErr <- server.Start(ready)
		}()
		select {
		case err := <-dashErr:
			if err != nil && strings.Contains(err.Error(), "address already in use") {
				logger.Error("Embedded dashboard failed: port %s already in use (override via config.dashboard.addr or --dashboard-addr)", dashboardAddr)
			} else if err != nil {
				logger.Error("Embedded dashboard failed: %v", err)
			}
			return 1
		case actual := <-ready:
			logger.Console("\n━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n📊 Embedded dashboard ready\n   🔊 Listen : %s\n   🌐 Visit : %s\n━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━",
				actual, formatDashboardURL(actual))
			go func() {
				if err := <-dashErr; err != nil {
					logger.Warn("Embedded dashboard stopped: %v", err)
				}
			}()
		}
	}

	// Configure signal handling
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	// Start replicator
	errCh := make(chan error, 1)
	go func() {
		if err := replicator.Start(); err != nil {
			errCh <- err
			return
		}
		// Replicator exited normally (SnapshotOnly mode or graceful shutdown via Ctrl+C)
		logger.Console("\n⌨️  Replicator stopped")
	}()

	// Wait for error or signal
	select {
	case err := <-errCh:
		// CRITICAL: Call Stop() to gracefully close connections before exiting
		// This prevents triggering Dragonfly v1.36.0 cleanup bug (see docs/zh/Dragonfly-v1.36.0-Bug-Workaround.md)
		// Without graceful shutdown, connections close abruptly (TCP RST) which can crash Dragonfly
		logger.Error("❌ Replication failed: %v", err)
		logger.Console("\n🔌 Gracefully closing connections to avoid crashing Dragonfly...")
		replicator.Stop() // Send FIN instead of RST
		logger.Console("\n📄 Check logs for details: %s", logger.GetLogFilePath())
		return 1
	case sig := <-sigCh:
		logger.Console("\n📡 Signal %v received, shutting down...", sig)
		replicator.Stop()
		return 0
	}
}

func runCheck(args []string) int {
	fs := flag.NewFlagSet("check", flag.ContinueOnError)
	fs.SetOutput(os.Stdout)
	var (
		configPath string
		mode       string
		qps        int
		parallel   int
		resultDir  string

		filterList      string
		compareTimes    int
		interval        int
		bigKeyThreshold int
		logFile         string
		logLevel        string
		maxKeys         int
	)
	fs.StringVar(&configPath, "config", "", "Configuration file path (YAML)")
	fs.StringVar(&configPath, "c", "", "Configuration file path (YAML)")
	fs.StringVar(&mode, "mode", "outline", "Validation mode: full/length/outline/smart")
	fs.IntVar(&qps, "qps", 500, "QPS limit")
	fs.IntVar(&parallel, "parallel", 4, "Parallelism")
	fs.StringVar(&resultDir, "result-dir", "./check-results", "Result output directory")

	fs.StringVar(&filterList, "filter", "", "Key filter list with prefix matching (e.g. 'user:*|session:*')")
	fs.IntVar(&compareTimes, "compare-times", 3, "Number of comparison rounds")
	fs.IntVar(&interval, "interval", 5, "Interval between rounds (seconds)")
	fs.IntVar(&bigKeyThreshold, "big-key-threshold", 524288, "Big-key threshold in bytes (smart mode only)")
	fs.StringVar(&logFile, "log-file", "", "Log file path")
	fs.StringVar(&logLevel, "log-level", "info", "Log level: debug/info/warn/error")
	fs.IntVar(&maxKeys, "max-keys", 0, "Maximum keys to validate (0 = unlimited)")

	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		log.Printf("Failed to parse arguments: %v", err)
		return 1
	}
	if configPath == "" {
		log.Println("The --config flag is required")
		fs.Usage()
		return 2
	}

	cfg, err := config.Load(configPath)
	if err != nil {
		log.Printf("Failed to load config: %v", err)
		return 2
	}

	// Build checker configuration
	checkerMode := checker.ModeKeyOutline
	switch mode {
	case "full":
		checkerMode = checker.ModeFullValue
	case "length":
		checkerMode = checker.ModeValueLength
	case "outline":
		checkerMode = checker.ModeKeyOutline
	case "smart":
		checkerMode = checker.ModeSmartBigKey
	default:
		log.Printf("Unknown validation mode: %s", mode)
		return 2
	}

	checkerCfg := checker.Config{
		SourceAddr:      cfg.Source.Addr,
		SourcePassword:  cfg.Source.Password,
		TargetAddr:      cfg.Target.Addr,
		TargetPassword:  cfg.Target.Password,
		Mode:            checkerMode,
		QPS:             qps,
		Parallel:        parallel,
		ResultDir:       resultDir,
		FilterList:      filterList,
		CompareTimes:    compareTimes,
		Interval:        interval,
		BigKeyThreshold: bigKeyThreshold,
		LogFile:         logFile,
		LogLevel:        logLevel,
		MaxKeys:         maxKeys,
		TaskName:        cfg.TaskName,
	}

	// Instantiate checker
	c := checker.NewChecker(checkerCfg)

	// Run comparison
	ctx := context.Background()
	result, err := c.Run(ctx, nil)
	if err != nil {
		log.Printf("Validation failed: %v", err)
		return 1
	}

	// Print summary
	c.PrintResult(result)

	// Non-zero exit code on inconsistency
	if result.InconsistentKeys > 0 {
		return 1
	}

	return 0
}

func printUsage() {
	binary := filepath.Base(os.Args[0])
	fmt.Printf(`df2redis - Dragonfly → Redis migration tool (prototype)

Usage:
  %[1]s <command> [options]

Available commands:
  prepare    Pre-check environment, dependencies, and config
  migrate    Run the migration pipeline (supports --dry-run)
  replicate  Start the Dragonfly replicator (handshake test)
  check      Validate data consistency (redis-full-check)
  status     Show current migration status
  rollback   Trigger rollback back to Dragonfly
  dashboard  Launch standalone dashboard
  help       Show this help
  version    Show version info

Examples:
  %[1]s migrate --config examples/migrate.sample.yaml --dry-run
  %[1]s replicate --config examples/migrate.sample.yaml
  %[1]s check --config examples/migrate.sample.yaml --mode outline
`, binary)
}

// initLogger configures project logging
// mode is the command name, e.g. replicate/migrate/check.
func initLogger(cfg *config.Config, mode string) error {
	// Parse log level
	level := parseLogLevel(cfg.Log.Level)

	// Resolve log directory
	logDir := cfg.ResolvePath(cfg.Log.Dir)

	// Build file prefix
	logFilePrefix := buildLogFilePrefix(cfg, mode)

	// Smart console detection: disable console output if stdout is not a TTY
	// This automatically handles background execution (nohup, systemd, Docker, etc.)
	consoleEnabled := cfg.Log.ConsoleEnabledValue()
	if consoleEnabled {
		// Check if stdout is a terminal
		if fileInfo, _ := os.Stdout.Stat(); (fileInfo.Mode() & os.ModeCharDevice) == 0 {
			// stdout is not a TTY (redirected to file or pipe)
			consoleEnabled = false
			log.Printf("[df2redis] Detected non-TTY stdout, disabling console output (logs will continue to file)")
		}
	}

	// Initialize logger
	if err := logger.Init(logDir, level, logFilePrefix, consoleEnabled); err != nil {
		return fmt.Errorf("Failed to initialize logger: %w", err)
	}
	log.SetOutput(logger.Writer())

	return nil
}

// buildLogFilePrefix returns a log file prefix.
// Format:
// - taskName provided: {taskName}_{mode}
// - otherwise: {sourceType}_{sourceIP}_{sourcePort}_{mode}
func buildLogFilePrefix(cfg *config.Config, mode string) string {
	// Use task name when supplied
	if cfg.TaskName != "" {
		return fmt.Sprintf("%s_%s", cfg.TaskName, mode)
	}

	// Otherwise derive from source address
	// Example: "192.168.1.100:16379" -> "dragonfly_192.168.1.100_16379"
	sourceType := cfg.Source.Type
	if sourceType == "" {
		sourceType = "dragonfly"
	}

	addr := cfg.Source.Addr
	// Replace ":" and "." with "_"
	addr = strings.ReplaceAll(addr, ":", "_")
	addr = strings.ReplaceAll(addr, ".", "_")

	if addr == "" {
		return fmt.Sprintf("%s_%s", sourceType, mode)
	}
	return fmt.Sprintf("%s_%s_%s", sourceType, addr, mode)
}

// parseLogLevel normalizes log level text
func parseLogLevel(levelStr string) logger.Level {
	switch strings.ToLower(strings.TrimSpace(levelStr)) {
	case "debug":
		return logger.DEBUG
	case "info":
		return logger.INFO
	case "warn", "warning":
		return logger.WARN
	case "error":
		return logger.ERROR
	default:
		return logger.INFO
	}
}
