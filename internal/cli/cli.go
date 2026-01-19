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
	"df2redis/internal/executor/shake"
	"df2redis/internal/logger"
	"df2redis/internal/pipeline"
	"df2redis/internal/replica"
	"df2redis/internal/state"
	"df2redis/internal/web"
)

// Execute dispatches CLI subcommands.
func Execute(args []string) int {
	log.SetFlags(log.LstdFlags | log.Lmsgprefix)
	log.SetPrefix("[df2redis] ")

	if len(args) == 0 {
		printUsage()
		return 1
	}

	switch args[0] {
	case "prepare":
		return runPrepare(args[1:])
	case "migrate":
		return runMigrate(args[1:])
	case "cold-import":
		return runColdImport(args[1:])
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
	case "compare-keys":
		return runCompareKeys(args[1:])
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
	fs.StringVar(&configPath, "config", "", "Configuration file path (YAML)")
	fs.StringVar(&configPath, "c", "", "Configuration file path (YAML)")
	fs.BoolVar(&dryRun, "dry-run", false, "Validate configuration only without running migration")
	fs.IntVar(&showPort, "show", 0, "Start embedded dashboard on the given port (e.g. --show 8080)")
	fs.StringVar(&showAddr, "show-addr", "", "Start embedded dashboard on the given address (e.g. --show-addr 0.0.0.0:8080)")

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

	// runCtx not needed anymore as Replicator manages its own context/signals
	// runCtx, stopSignals := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	// defer stopSignals()

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
		return 0
	case sig := <-sigCh:
		logger.Console("\n📡 Signal %v received, shutting down...", sig)
		replicator.Stop()
		return 0
	}
}

func runColdImport(args []string) int {
	fs := flag.NewFlagSet("cold-import", flag.ContinueOnError)
	fs.SetOutput(os.Stdout)
	var configPath string
	var rdbPath string
	var shakeBinary string
	var shakeConfig string
	var shakeArgs string
	var taskNameFlag string
	fs.StringVar(&configPath, "config", "", "Configuration file path (YAML)")
	fs.StringVar(&configPath, "c", "", "Configuration file path (YAML)")
	fs.StringVar(&rdbPath, "rdb", "", "Existing RDB file path (overrides migrate.snapshotPath)")
	fs.StringVar(&shakeBinary, "shake-binary", "", "redis-shake binary path (overrides migrate.shakeBinary)")
	fs.StringVar(&shakeConfig, "shake-conf", "", "redis-shake config file (overrides migrate.shakeConfigFile)")
	fs.StringVar(&shakeArgs, "shake-args", "", "redis-shake runtime args (overrides migrate.shakeArgs)")
	fs.StringVar(&taskNameFlag, "task-name", "", "Task name for log file prefix")

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

	if taskNameFlag != "" {
		cfg.TaskName = taskNameFlag
	}

	// Partial validation for cold-import (Source is not required)
	var errs []string
	if cfg.Target.Addr == "" && len(cfg.Target.Cluster.Seeds) == 0 {
		errs = append(errs, "target.addr or target.cluster.seeds is required")
	}
	if cfg.Migrate.SnapshotPath == "" && cfg.Migrate.ShakeConfigFile == "" && rdbPath == "" && shakeConfig == "" {
		errs = append(errs, "migrate.snapshotPath or shake-conf is required")
	}
	if cfg.Migrate.ShakeBinary == "" && shakeBinary == "" {
		errs = append(errs, "migrate.shakeBinary is required")
	}
	if len(errs) > 0 {
		log.Printf("Config validation failed for cold-import:\n - %s", strings.Join(errs, "\n - "))
		return 2
	}

	if rdbPath != "" {
		cfg.Migrate.SnapshotPath = rdbPath
	}
	if shakeBinary != "" {
		cfg.Migrate.ShakeBinary = shakeBinary
	}
	if shakeConfig != "" {
		cfg.Migrate.ShakeConfigFile = shakeConfig
	}
	if shakeArgs != "" {
		cfg.Migrate.ShakeArgs = shakeArgs
	}

	if err := cfg.EnsureStateDir(); err != nil {
		log.Printf("Failed to create state directory: %v", err)
		return 1
	}

	migrateCfg := cfg.ResolvedMigrateConfig()
	cfg.Migrate = migrateCfg

	// Validation: RDB path is required UNLESS a shake config file is provided (which might contain it)
	if cfg.Migrate.SnapshotPath == "" && cfg.Migrate.ShakeConfigFile == "" {
		log.Println("migrate.snapshotPath is not configured (and no shake-conf provided)")
		return 2
	}
	if cfg.Migrate.ShakeBinary == "" {
		log.Println("migrate.shakeBinary is not configured")
		return 2
	}

	if err := initLogger(cfg, "cold-import"); err != nil {
		log.Printf("Failed to initialize logging: %v", err)
		return 1
	}
	defer logger.Close()

	if cfg.Migrate.SnapshotPath != "" {
		if _, err := os.Stat(cfg.Migrate.SnapshotPath); err != nil {
			logger.Error("RDB file unavailable: %v", err)
			return 1
		}
	}
	if _, err := os.Stat(cfg.Migrate.ShakeBinary); err != nil {
		logger.Error("redis-shake binary unavailable: %v", err)
		return 1
	}

	if strings.TrimSpace(cfg.Migrate.ShakeArgs) == "" && strings.TrimSpace(cfg.Migrate.ShakeConfigFile) == "" {
		path, err := pipeline.GenerateShakeConfigFile(cfg, cfg.ResolveStateDir())
		if err != nil {
			logger.Error("Failed to generate redis-shake config: %v", err)
			return 1
		}
		logger.Console("🛠️ Generated redis-shake config: %s", path)
	}

	importer, err := shake.NewImporter(cfg.Migrate, cfg.Target)
	if err != nil {
		logger.Error("Failed to initialize redis-shake: %v", err)
		return 1
	}

	logger.Console("❄️ Cold import started")
	if cfg.Migrate.SnapshotPath != "" {
		logger.Console("📦 RDB file: %s", cfg.Migrate.SnapshotPath)
	} else {
		logger.Console("📦 RDB file: (configured in shake-conf)")
	}
	logger.Console("🎯 Target: %s @ %s", cfg.Target.Type, cfg.Target.Addr)
	logger.Console("⚙️ redis-shake: %s", cfg.Migrate.ShakeBinary)
	logger.Console("⚠️ Existing data on the target may be overwritten")

	if err := importer.Run(context.Background()); err != nil {
		logger.Error("cold-import failed: %v", err)
		return 1
	}
	logger.Console("✅ Cold import completed")
	return 0
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
			Addr:            dashboardAddr,
			Cfg:             cfg,
			Store:           store,
			HistoryProvider: replicator.GetHistoryStore,
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
		// Keep running after handshake until interrupted
		logger.Console("\n⌨️  Press Ctrl+C to stop the replicator")
		select {}
	}()

	// Wait for error or signal
	select {
	case err := <-errCh:
		logger.Error("❌ Replicator failed to start: %v", err)
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
		configPath      string
		mode            string
		qps             int
		parallel        int
		resultDir       string
		binary          string
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
	fs.StringVar(&binary, "binary", "redis-full-check", "redis-full-check binary path")
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
		BinaryPath:      binary,
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
	result, err := c.Run(ctx)
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
  cold-import Use redis-shake once to import an RDB into target Redis
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

	// Initialize logger
	if err := logger.Init(logDir, level, logFilePrefix, cfg.Log.ConsoleEnabledValue()); err != nil {
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
