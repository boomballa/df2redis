package logger

import (
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Level 日志级别
type Level int

const (
	DEBUG Level = iota
	INFO
	WARN
	ERROR
)

var levelNames = map[Level]string{
	DEBUG: "DEBUG",
	INFO:  "INFO",
	WARN:  "WARN",
	ERROR: "ERROR",
}

// Logger 日志器
type Logger struct {
	mu          sync.Mutex
	fileLogger  *log.Logger // 文件日志
	consoleLog  *log.Logger // 控制台日志（只输出关键信息）
	level       Level
	logFile     *os.File
	logFilePath string
}

var (
	defaultLogger *Logger
	once          sync.Once
)

// Init 初始化全局日志器
// logFilePrefix: 日志文件前缀，例如 "df2redis-test_replicate" 或 "dragonfly_10.46.128.12_7380_replicate"
func Init(logDir string, level Level, logFilePrefix string) error {
	var initErr error
	once.Do(func() {
		// 创建日志目录
		if err := os.MkdirAll(logDir, 0755); err != nil {
			initErr = fmt.Errorf("创建日志目录失败: %w", err)
			return
		}

		// 日志文件路径：logs/{prefix}.log
		// 如果没有提供前缀，使用默认的 "df2redis"
		if logFilePrefix == "" {
			logFilePrefix = "df2redis"
		}
		logFileName := fmt.Sprintf("%s.log", logFilePrefix)
		logFilePath := filepath.Join(logDir, logFileName)

		// 打开日志文件（追加模式）
		logFile, err := os.OpenFile(logFilePath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
		if err != nil {
			initErr = fmt.Errorf("打开日志文件失败: %w", err)
			return
		}

		// 创建文件日志器（写入所有日志）
		fileLogger := log.New(logFile, "", 0) // 不使用默认前缀，自己格式化

		// 创建控制台日志器（只输出关键信息）
		consoleLog := log.New(os.Stdout, "", 0)

		defaultLogger = &Logger{
			fileLogger:  fileLogger,
			consoleLog:  consoleLog,
			level:       level,
			logFile:     logFile,
			logFilePath: logFilePath,
		}
	})
	return initErr
}

// Close 关闭日志器
func Close() error {
	if defaultLogger != nil && defaultLogger.logFile != nil {
		return defaultLogger.logFile.Close()
	}
	return nil
}

// GetLogFilePath 获取日志文件路径
func GetLogFilePath() string {
	if defaultLogger != nil {
		return defaultLogger.logFilePath
	}
	return ""
}

// 格式化日志消息
func formatMessage(level Level, format string, args ...interface{}) string {
	timestamp := time.Now().Format("2006/01/02 15:04:05")
	levelStr := levelNames[level]
	message := fmt.Sprintf(format, args...)
	return fmt.Sprintf("%s [%s] %s", timestamp, levelStr, message)
}

// logToFile 写入文件日志
func logToFile(level Level, format string, args ...interface{}) {
	if defaultLogger == nil {
		return
	}
	if level < defaultLogger.level {
		return
	}
	defaultLogger.mu.Lock()
	defer defaultLogger.mu.Unlock()
	message := formatMessage(level, format, args...)
	defaultLogger.fileLogger.Println(message)
}

// logToConsole 写入控制台（关键信息）
func logToConsole(format string, args ...interface{}) {
	if defaultLogger == nil {
		fmt.Printf(format+"\n", args...)
		return
	}
	defaultLogger.mu.Lock()
	defer defaultLogger.mu.Unlock()
	// 控制台输出使用原始格式（保持现有的 emoji 和格式）
	timestamp := time.Now().Format("2006/01/02 15:04:05")
	message := fmt.Sprintf(format, args...)
	defaultLogger.consoleLog.Printf("%s [df2redis] %s", timestamp, message)
}

// logToBoth 同时写入文件和控制台
func logToBoth(level Level, format string, args ...interface{}) {
	logToFile(level, format, args...)
	logToConsole(format, args...)
}

// Debug 输出调试日志（仅文件）
func Debug(format string, args ...interface{}) {
	logToFile(DEBUG, format, args...)
}

// Info 输出信息日志（仅文件）
func Info(format string, args ...interface{}) {
	logToFile(INFO, format, args...)
}

// Warn 输出警告日志（文件 + 控制台）
func Warn(format string, args ...interface{}) {
	logToBoth(WARN, format, args...)
}

// Error 输出错误日志（文件 + 控制台）
func Error(format string, args ...interface{}) {
	logToBoth(ERROR, format, args...)
}

// Console 只输出到控制台（用于关键进度信息）
func Console(format string, args ...interface{}) {
	logToConsole(format, args...)
	// 同时也写入文件
	logToFile(INFO, format, args...)
}

// Printf 兼容标准 log.Printf（写入文件 + 控制台）
func Printf(format string, args ...interface{}) {
	logToBoth(INFO, format, args...)
}

// Println 兼容标准 log.Println（写入文件 + 控制台）
func Println(args ...interface{}) {
	message := fmt.Sprint(args...)
	logToBoth(INFO, "%s", message)
}

// Writer 返回一个 io.Writer，用于替换标准 log 包
func Writer() io.Writer {
	if defaultLogger != nil {
		return defaultLogger.logFile
	}
	return os.Stdout
}
