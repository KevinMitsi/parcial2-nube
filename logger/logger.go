package logger

import (
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"
)

type LogLevel int

const (
	DEBUG LogLevel = iota
	INFO
	WARN
	ERROR
)

var (
	levelNames = map[LogLevel]string{
		DEBUG: "DEBUG",
		INFO:  "INFO",
		WARN:  "WARN",
		ERROR: "ERROR",
	}
)

type Logger struct {
	mu          sync.Mutex
	file        *os.File
	logger      *log.Logger
	level       LogLevel
	maxFileSize int64
	operationID string
}

var instance *Logger
var once sync.Once

func Init(logDir string, maxFileSizeMB int) error {
	var err error
	once.Do(func() {
		if err = os.MkdirAll(logDir, 0755); err != nil {
			return
		}

		logPath := filepath.Join(logDir, fmt.Sprintf("app_%s.log", time.Now().Format("2006-01-02")))
		file, fileErr := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
		if fileErr != nil {
			err = fileErr
			return
		}

		multiWriter := io.MultiWriter(os.Stdout, file)

		instance = &Logger{
			file:        file,
			logger:      log.New(multiWriter, "", 0),
			level:       DEBUG,
			maxFileSize: int64(maxFileSizeMB) * 1024 * 1024,
		}
	})
	return err
}

func Get() *Logger {
	if instance == nil {
		panic("Logger no inicializado. Llama a logger.Init() primero")
	}
	return instance
}

func (l *Logger) SetOperationID(id string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.operationID = id
}

func (l *Logger) log(level LogLevel, format string, args ...interface{}) {
	if level < l.level {
		return
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	timestamp := time.Now().Format("2006-01-02 15:04:05.000")
	levelStr := levelNames[level]

	_, file, line, _ := runtime.Caller(2)
	file = filepath.Base(file)

	opID := ""
	if l.operationID != "" {
		opID = fmt.Sprintf("[%s] ", l.operationID)
	}

	message := fmt.Sprintf(format, args...)
	logLine := fmt.Sprintf("[%s] [%s] %s%s:%d - %s\n", timestamp, levelStr, opID, file, line, message)

	l.logger.Print(logLine)

	// Rotar archivo si es necesario
	l.rotateIfNeeded()
}

func (l *Logger) rotateIfNeeded() {
	info, err := l.file.Stat()
	if err != nil {
		return
	}

	if info.Size() >= l.maxFileSize {
		l.file.Close()

		oldPath := l.file.Name()
		newPath := fmt.Sprintf("%s.%d", oldPath, time.Now().Unix())
		os.Rename(oldPath, newPath)

		file, err := os.OpenFile(oldPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
		if err != nil {
			return
		}

		l.file = file
		multiWriter := io.MultiWriter(os.Stdout, file)
		l.logger.SetOutput(multiWriter)
	}
}

func (l *Logger) Debug(format string, args ...interface{}) {
	l.log(DEBUG, format, args...)
}

func (l *Logger) Info(format string, args ...interface{}) {
	l.log(INFO, format, args...)
}

func (l *Logger) Warn(format string, args ...interface{}) {
	l.log(WARN, format, args...)
}

func (l *Logger) Error(format string, args ...interface{}) {
	l.log(ERROR, format, args...)
}

func (l *Logger) Fatal(format string, args ...interface{}) {
	l.log(ERROR, format, args...)
	os.Exit(1)
}

func (l *Logger) LogOperation(operation, vmName, user string) {
	l.Info("Operación iniciada: %s | VM: %s | Usuario: %s", operation, vmName, user)
}

func (l *Logger) LogOperationStep(step string, duration time.Duration) {
	l.Info("Paso completado: %s | Duración: %v", step, duration)
}

func (l *Logger) LogOperationComplete(operation string, duration time.Duration, resources string) {
	l.Info("Operación completada: %s | Duración total: %v | Recursos: %s", operation, duration, resources)
}

func (l *Logger) LogOperationError(operation, step string, err error) {
	l.Error("Operación fallida: %s | Paso: %s | Error: %v", operation, step, err)

	// Stack trace
	buf := make([]byte, 4096)
	n := runtime.Stack(buf, false)
	l.Error("Stack trace:\n%s", string(buf[:n]))
}

func (l *Logger) LogVBoxCommand(cmd string, args []string, duration time.Duration, output string, err error) {
	if err != nil {
		l.Error("VBoxManage falló | Comando: %s %v | Duración: %v | Error: %v | Output: %s",
			cmd, args, duration, err, output)
	} else {
		l.Debug("VBoxManage exitoso | Comando: %s %v | Duración: %v | Output: %s",
			cmd, args, duration, output)
	}
}

func (l *Logger) Close() {
	if l.file != nil {
		l.file.Close()
	}
}
