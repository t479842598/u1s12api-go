// Package logging 提供分级日志：stdout 输出 + 内存环形缓冲（供管理后台查看）
// + 可选文件持久化（data/logs/app.log）。
package logging

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Entry 一条日志记录。
type Entry struct {
	ID    int64  `json:"id"`
	TS    string `json:"ts"`
	Level string `json:"level"`
	Name  string `json:"name"`
	Msg   string `json:"msg"`
}

type level int

const (
	levelDebug level = iota
	levelInfo
	levelWarn
	levelError
)

func parseLevel(s string) level {
	switch strings.ToLower(s) {
	case "debug":
		return levelDebug
	case "warn", "warning":
		return levelWarn
	case "error":
		return levelError
	default:
		return levelInfo
	}
}

func (l level) String() string {
	switch l {
	case levelDebug:
		return "DEBUG"
	case levelWarn:
		return "WARN"
	case levelError:
		return "ERROR"
	default:
		return "INFO"
	}
}

var (
	mu       sync.Mutex
	ring     []Entry
	ringCap  = 2000
	nextID   atomic.Int64
	minLvl   atomic.Int64
	fileOut  io.Writer
	stdColor bool
)

// Configure 初始化日志：级别、颜色、可选文件输出。
func Configure(levelStr string, color bool, logFilePath string) {
	minLvl.Store(int64(parseLevel(levelStr)))
	stdColor = color
	if logFilePath != "" {
		_ = os.MkdirAll(filepath.Dir(logFilePath), 0o755)
		f, err := os.OpenFile(logFilePath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
		if err == nil {
			fileOut = f
		}
	}
}

// Logger 命名日志器。
type Logger struct {
	name string
}

// Named 创建命名日志器。
func Named(name string) *Logger {
	return &Logger{name: name}
}

func (l *Logger) Debugf(format string, args ...any) { l.log(levelDebug, format, args...) }
func (l *Logger) Infof(format string, args ...any)  { l.log(levelInfo, format, args...) }
func (l *Logger) Warnf(format string, args ...any)  { l.log(levelWarn, format, args...) }
func (l *Logger) Errorf(format string, args ...any) { l.log(levelError, format, args...) }

func (l *Logger) log(lvl level, format string, args ...any) {
	if int64(lvl) < minLvl.Load() {
		return
	}
	msg := fmt.Sprintf(format, args...)
	now := time.Now().Format("2006-01-02 15:04:05")
	e := Entry{ID: nextID.Add(1), TS: now, Level: lvl.String(), Name: l.name, Msg: msg}

	line := fmt.Sprintf("%s [%s] %s: %s\n", e.TS, e.Level, e.Name, e.Msg)
	fmt.Fprint(os.Stdout, colorize(lvl, line))

	mu.Lock()
	ring = append(ring, e)
	if len(ring) > ringCap {
		ring = ring[len(ring)-ringCap:]
	}
	mu.Unlock()

	if fileOut != nil {
		fmt.Fprint(fileOut, line)
	}
}

func colorize(lvl level, s string) string {
	if !stdColor {
		return s
	}
	var c string
	switch lvl {
	case levelDebug:
		c = "\033[90m"
	case levelWarn:
		c = "\033[33m"
	case levelError:
		c = "\033[31m"
	default:
		c = "\033[36m"
	}
	return c + s + "\033[0m"
}

// Recent 返回 id > sinceID 的最近日志，最多 limit 条。
func Recent(sinceID int64, limit int, levelStr string) []Entry {
	min := int64(-1)
	if levelStr != "" && levelStr != "all" {
		min = int64(parseLevel(levelStr))
	}
	mu.Lock()
	defer mu.Unlock()
	out := make([]Entry, 0, limit)
	start := 0
	if len(ring) > limit {
		start = len(ring) - limit
	}
	for _, e := range ring[start:] {
		if e.ID <= sinceID {
			continue
		}
		if min >= 0 && levelRank(e.Level) < min {
			continue
		}
		out = append(out, e)
	}
	return out
}

func levelRank(s string) int64 {
	return int64(parseLevel(strings.ToLower(mapLegacy(s))))
}

func mapLegacy(s string) string { return s }
