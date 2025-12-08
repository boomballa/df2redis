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

// Level lists supported log severities
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

// Logger writes to file plus console
type Logger struct {
	mu          sync.Mutex
	fileLogger  *log.Logger // file output
	consoleLog  *log.Logger // console highlights
	level       Level
	logFile     *os.File
	logFilePath string
}

var (
	defaultLogger *Logger
	once          sync.Once
)

// Init creates the global logger.
// logFilePrefix examples: "df2redis-test_replicate" or "dragonfly_10.46.128.12_7380_replicate".
func Init(logDir string, level Level, logFilePrefix string) error {
	var initErr error
	once.Do(func() {
		// Ensure log directory exists
		if err := os.MkdirAll(logDir, 0755); err != nil {
			initErr = fmt.Errorf("创建日志目录失败: %w", err)
			return
		}

		// Build logs/{prefix}.log, fallback prefix df2redis
		if logFilePrefix == "" {
			logFilePrefix = "df2redis"
		}
		logFileName := fmt.Sprintf("%s.log", logFilePrefix)
		logFilePath := filepath.Join(logDir, logFileName)

		// Open log file in append mode
		logFile, err := os.OpenFile(logFilePath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
		if err != nil {
			initErr = fmt.Errorf("打开日志文件失败: %w", err)
			return
		}

		// File logger (custom formatter)
		fileLogger := log.New(logFile, "", 0)

		// Console logger (key info only)
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

// Close shuts down the log file
func Close() error {
	if defaultLogger != nil && defaultLogger.logFile != nil {
		return defaultLogger.logFile.Close()
	}
	return nil
}

// GetLogFilePath returns the backing log file path
func GetLogFilePath() string {
	if defaultLogger != nil {
		return defaultLogger.logFilePath
	}
	return ""
}

// formatMessage standardizes log records
func formatMessage(level Level, format string, args ...interface{}) string {
	timestamp := time.Now().Format("2006/01/02 15:04:05")
	levelStr := levelNames[level]
	message := fmt.Sprintf(format, args...)
	return fmt.Sprintf("%s [%s] %s", timestamp, levelStr, message)
}

// logToFile writes to the log file
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

// logToConsole prints highlights to stdout
func logToConsole(format string, args ...interface{}) {
	if defaultLogger == nil {
		fmt.Printf(format+"\n", args...)
		return
	}
	defaultLogger.mu.Lock()
	defer defaultLogger.mu.Unlock()
	// Preserve the original format/emojis on console output
	timestamp := time.Now().Format("2006/01/02 15:04:05")
	message := fmt.Sprintf(format, args...)
	defaultLogger.consoleLog.Printf("%s [df2redis] %s", timestamp, message)
}

// logToBoth mirrors the entry to both sinks
func logToBoth(level Level, format string, args ...interface{}) {
	logToFile(level, format, args...)
	logToConsole(format, args...)
}

// Debug logs debug messages (file only)
func Debug(format string, args ...interface{}) {
	logToFile(DEBUG, format, args...)
}

// Info logs info messages (file only)
func Info(format string, args ...interface{}) {
	logToFile(INFO, format, args...)
}

// Warn logs warnings (file + console)
func Warn(format string, args ...interface{}) {
	logToBoth(WARN, format, args...)
}

// Error logs errors (file + console)
func Error(format string, args ...interface{}) {
	logToBoth(ERROR, format, args...)
}

// Console prints status lines to console and mirrors to file
func Console(format string, args ...interface{}) {
	logToConsole(format, args...)
	// Mirror into file for auditing
	logToFile(INFO, format, args...)
}

// Printf mimics log.Printf (file + console)
func Printf(format string, args ...interface{}) {
	logToBoth(INFO, format, args...)
}

// Println mimics log.Println (file + console)
func Println(args ...interface{}) {
	message := fmt.Sprint(args...)
	logToBoth(INFO, "%s", message)
}

// Writer returns an io.Writer compatible with the standard log package
func Writer() io.Writer {
	if defaultLogger != nil {
		return defaultLogger.logFile
	}
	return os.Stdout
}
