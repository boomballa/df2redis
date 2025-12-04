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
		log.Printf("未知子命令: %s", args[0])
		printUsage()
		return 1
	}
}

func runPrepare(args []string) int {
	cfg, err := loadConfigFromArgs("prepare", args)
	if err != nil {
		return errorToExitCode(err)
	}
	if err := cfg.EnsureStateDir(); err != nil {
		log.Printf("创建状态目录失败: %v", err)
		return 1
	}
	log.Printf("🛠️ 准备阶段完成:\n  📂 stateDir  : %s\n  📝 statusFile: %s",
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
	fs.StringVar(&configPath, "config", "", "配置文件路径 (YAML)")
	fs.StringVar(&configPath, "c", "", "配置文件路径 (YAML)")
	fs.BoolVar(&dryRun, "dry-run", false, "仅校验配置，不执行真实迁移")
	fs.IntVar(&showPort, "show", 0, "启动内置仪表盘并监听指定端口 (例如 --show 8080)")
	fs.StringVar(&showAddr, "show-addr", "", "启动内置仪表盘并监听指定地址 (例如 --show-addr 0.0.0.0:8080)")

	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		log.Printf("解析参数失败: %v", err)
		return 1
	}
	if configPath == "" {
		log.Println("必须提供 --config")
		fs.Usage()
		return 2
	}

	cfg, err := config.Load(configPath)
	if err != nil {
		log.Printf("配置加载失败: %v", err)
		return 2
	}
	log.Printf("✅ 配置加载成功:\n%s", cfg.PrettySummary())

	if dryRun {
		log.Println("🚧 dry-run 模式：仅校验配置，不执行真实迁移。")
		return 0
	}

	runCtx, stopSignals := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopSignals()

	if err := cfg.EnsureStateDir(); err != nil {
		log.Printf("创建状态目录失败: %v", err)
		return 1
	}

	store := state.NewStore(cfg.StatusFilePath())

	var dashboardAddr string
	if showAddr != "" {
		if !strings.Contains(showAddr, ":") {
			if showPort > 0 {
				showAddr = fmt.Sprintf("%s:%d", showAddr, showPort)
			} else {
				log.Printf("show-addr 必须包含端口，例如 0.0.0.0:8080")
				return 2
			}
		}
		if _, _, err := net.SplitHostPort(showAddr); err != nil {
			log.Printf("show-addr 格式不合法: %v", err)
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
				log.Printf("⚠️ 仪表盘初始化失败: %v", err)
				return
			}
			log.Printf("\n━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n📺 自动仪表盘已启动\n   🔊 监听 : %s\n   🌐 访问 : %s\n   ⌨️ 提示 : 按 Ctrl+C 结束仪表盘服务\n━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━", dashboardAddr, formatDashboardURL(dashboardAddr))
			if err := server.Start(); err != nil {
				log.Printf("dashboard 停止: %v", err)
			}
		}()
	}

	ctxObj, err := pipeline.NewContext(runCtx, cfg, store)
	if err != nil {
		log.Printf("初始化上下文失败: %v", err)
		return 1
	}
	defer ctxObj.Close()

	pl := pipeline.New().
		Add(pipeline.NewPrecheckStage()).
		Add(pipeline.NewShakeConfigStage()).
		Add(pipeline.NewBgsaveStage()).
		Add(pipeline.NewImportStage()).
		Add(pipeline.NewIncrementalPlaceholderStage())

	if ok := pl.Run(ctxObj); !ok {
		log.Println("迁移管线执行失败，详情见日志。")
		return 1
	}

	log.Println("迁移管线执行结束。")
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
		log.Printf("读取状态文件失败: %v", err)
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
	if err := store.SetPipelineStatus("rolling_back", "开始回滚流程"); err != nil {
		log.Printf("更新状态失败: %v", err)
		return 1
	}
	if err := store.SetPipelineStatus("rolled_back", "回滚已标记，待人工切回"); err != nil {
		log.Printf("更新状态失败: %v", err)
		return 1
	}
	log.Printf("↩️ 已写入回滚标记，stateDir=%s", cfg.ResolveStateDir())
	return 0
}

func runDashboard(args []string) int {
	fs := flag.NewFlagSet("dashboard", flag.ContinueOnError)
	fs.SetOutput(os.Stdout)
	var (
		configPath string
		addr       string
	)
	fs.StringVar(&configPath, "config", "", "配置文件路径 (YAML)")
	fs.StringVar(&configPath, "c", "", "配置文件路径 (YAML)")
	fs.StringVar(&addr, "addr", ":8080", "仪表盘监听地址")

	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		log.Printf("解析参数失败: %v", err)
		return 1
	}
	if configPath == "" {
		fs.Usage()
		return 2
	}

	cfg, err := config.Load(configPath)
	if err != nil {
		log.Printf("配置加载失败: %v", err)
		return 2
	}
	store := state.NewStore(cfg.StatusFilePath())

	server, err := web.New(web.Options{
		Addr:  addr,
		Cfg:   cfg,
		Store: store,
	})
	if err != nil {
		log.Printf("初始化 dashboard 失败: %v", err)
		return 1
	}

	log.Printf("\n━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n📺 仪表盘已就绪\n   🔊 监听 : %s\n   🌐 访问 : %s\n   ⌨️ 提示 : 按 Ctrl+C 结束仪表盘服务\n━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━", addr, formatDashboardURL(addr))
	if err := server.Start(); err != nil {
		log.Printf("dashboard 停止: %v", err)
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
		return fmt.Sprintf("http://127.0.0.1:%s (或 http://<服务器IP>:%s)", port, port)
	}
	host, port, err := net.SplitHostPort(clean)
	if err != nil {
		return "http://" + clean
	}
	switch host {
	case "", "0.0.0.0", "::", "[::]":
		return fmt.Sprintf("http://<服务器IP>:%s (监听 %s:%s)", port, host, port)
	default:
		return fmt.Sprintf("http://%s:%s", host, port)
	}
}

func loadConfigFromArgs(cmd string, args []string) (*config.Config, error) {
	fs := flag.NewFlagSet(cmd, flag.ContinueOnError)
	fs.SetOutput(os.Stdout)
	var configPath string
	fs.StringVar(&configPath, "config", "", "配置文件路径 (YAML)")
	fs.StringVar(&configPath, "c", "", "配置文件路径 (YAML)")

	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return nil, flag.ErrHelp
		}
		return nil, fmt.Errorf("解析参数失败: %w", err)
	}
	if configPath == "" {
		fs.Usage()
		return nil, fmt.Errorf("必须提供 --config")
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
	log.Printf("命令执行失败: %v", err)
	return 1
}

func runReplicate(args []string) int {
	cfg, err := loadConfigFromArgs("replicate", args)
	if err != nil {
		return errorToExitCode(err)
	}

	// 创建复制器
	replicator := replica.NewReplicator(cfg)

	// 设置信号处理
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	// 启动复制器
	errCh := make(chan error, 1)
	go func() {
		if err := replicator.Start(); err != nil {
			errCh <- err
			return
		}
		// 握手成功后，保持运行等待信号
		log.Println("\n⌨️  按 Ctrl+C 停止复制器")
		select {}
	}()

	// 等待错误或信号
	select {
	case err := <-errCh:
		log.Printf("❌ 复制器启动失败: %v", err)
		return 1
	case sig := <-sigCh:
		log.Printf("\n📡 收到信号 %v，正在停止...", sig)
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
	)
	fs.StringVar(&configPath, "config", "", "配置文件路径 (YAML)")
	fs.StringVar(&configPath, "c", "", "配置文件路径 (YAML)")
	fs.StringVar(&mode, "mode", "outline", "校验模式: full/length/outline/smart")
	fs.IntVar(&qps, "qps", 500, "QPS 限制")
	fs.IntVar(&parallel, "parallel", 4, "并发度")
	fs.StringVar(&resultDir, "result-dir", "./check-results", "结果输出目录")
	fs.StringVar(&binary, "binary", "redis-full-check", "redis-full-check 二进制文件路径")
	fs.StringVar(&filterList, "filter", "", "Key 过滤列表，支持前缀匹配 (例如: 'user:*|session:*')")
	fs.IntVar(&compareTimes, "compare-times", 3, "对比轮次")
	fs.IntVar(&interval, "interval", 5, "每轮对比间隔(秒)")
	fs.IntVar(&bigKeyThreshold, "big-key-threshold", 524288, "大key阈值(字节)，仅smart模式生效")
	fs.StringVar(&logFile, "log-file", "", "日志文件路径")
	fs.StringVar(&logLevel, "log-level", "info", "日志级别: debug/info/warn/error")

	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		log.Printf("解析参数失败: %v", err)
		return 1
	}
	if configPath == "" {
		log.Println("必须提供 --config")
		fs.Usage()
		return 2
	}

	cfg, err := config.Load(configPath)
	if err != nil {
		log.Printf("配置加载失败: %v", err)
		return 2
	}

	// 构建 checker 配置
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
		log.Printf("未知的校验模式: %s", mode)
		return 2
	}

	checkerCfg := checker.Config{
		SourceAddr:      cfg.Source.Addr,
		SourcePassword:  cfg.Source.Password,
		TargetAddr:      cfg.Target.Seed,
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
	}

	// 创建 checker
	c := checker.NewChecker(checkerCfg)

	// 执行校验
	ctx := context.Background()
	result, err := c.Run(ctx)
	if err != nil {
		log.Printf("校验执行失败: %v", err)
		return 1
	}

	// 打印结果
	c.PrintResult(result)

	// 如果有不一致的 key，返回非零退出码
	if result.InconsistentKeys > 0 {
		return 1
	}

	return 0
}

func printUsage() {
	binary := filepath.Base(os.Args[0])
	fmt.Printf(`df2redis - Dragonfly → Redis 迁移工具 (原型)

用法:
  %[1]s <command> [options]

可用命令:
  prepare    预先检查环境、依赖与配置
  migrate    执行迁移流程 (支持 --dry-run)
  replicate  启动 Dragonfly 复制器（测试握手）
  check      数据一致性校验（基于 redis-full-check）
  status     查看当前迁移状态
  rollback   执行回滚到 Dragonfly 的流程
  dashboard  启动独立仪表盘
  help       显示此帮助
  version    显示版本信息

示例:
  %[1]s migrate --config examples/migrate.sample.yaml --dry-run
  %[1]s replicate --config examples/migrate.sample.yaml
  %[1]s check --config examples/migrate.sample.yaml --mode outline
`, binary)
}
