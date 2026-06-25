package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

var detailedLog *DetailedLogger

// DetailedLogger writes untruncated JSONL logs to agent.jsonl and llm.jsonl.
type DetailedLogger struct {
	agentFile *os.File
	llmFile   *os.File
	seq       atomic.Int64
	maxSize   int64
	runID     string
	mu        sync.Mutex
}

// NewDetailedLogger opens (or creates) agent.jsonl and llm.jsonl under path.
// When runID is non-empty, logs are written to a per-run subdirectory.
func NewDetailedLogger(path, runID string, maxSize int64) (*DetailedLogger, error) {
	if runID != "" {
		path = filepath.Join(path, runID)
	}
	if err := os.MkdirAll(path, 0o755); err != nil {
		return nil, fmt.Errorf("creating log dir: %w", err)
	}

	agentFile, err := os.OpenFile(filepath.Join(path, "agent.jsonl"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, fmt.Errorf("opening agent.jsonl: %w", err)
	}

	llmFile, err := os.OpenFile(filepath.Join(path, "llm.jsonl"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		agentFile.Close()
		return nil, fmt.Errorf("opening llm.jsonl: %w", err)
	}

	return &DetailedLogger{
		agentFile: agentFile,
		llmFile:   llmFile,
		maxSize:   maxSize,
		runID:     runID,
	}, nil
}

// LogAgent writes an untruncated event to agent.jsonl. No-op on nil receiver.
func (dl *DetailedLogger) LogAgent(event string, fields map[string]any) {
	if dl == nil {
		return
	}
	dl.writeEntry(true, event, fields)
}

// LogLLM writes an untruncated event to llm.jsonl. No-op on nil receiver.
func (dl *DetailedLogger) LogLLM(event string, fields map[string]any) {
	if dl == nil {
		return
	}
	dl.writeEntry(false, event, fields)
}

func (dl *DetailedLogger) writeEntry(isAgent bool, event string, fields map[string]any) {
	seq := dl.seq.Add(1)
	entry := map[string]any{
		"ts":     time.Now().Format(time.RFC3339Nano),
		"run_id": dl.runID,
		"seq":    seq,
		"event":  event,
	}
	for k, v := range fields {
		entry[k] = v
	}

	data, err := json.Marshal(entry)
	if err != nil {
		log.Printf("detailed_logging: marshal error: %v", err)
		return
	}
	data = append(data, '\n')

	dl.mu.Lock()
	defer dl.mu.Unlock()

	var f *os.File
	if isAgent {
		f = dl.agentFile
	} else {
		f = dl.llmFile
	}
	if f == nil {
		return
	}

	if _, err := f.Write(data); err != nil {
		log.Printf("detailed_logging: write error: %v", err)
		return
	}

	if info, err := f.Stat(); err == nil && info.Size() > dl.maxSize {
		dl.rotate()
	}
}

// Close flushes and closes both log files. No-op on nil receiver.
func (dl *DetailedLogger) Close() {
	if dl == nil {
		return
	}
	dl.mu.Lock()
	defer dl.mu.Unlock()
	if dl.agentFile != nil {
		_ = dl.agentFile.Sync()
		_ = dl.agentFile.Close()
		dl.agentFile = nil
	}
	if dl.llmFile != nil {
		_ = dl.llmFile.Sync()
		_ = dl.llmFile.Close()
		dl.llmFile = nil
	}
}

// rotate renames each oversize file to .1.jsonl and reopens fresh.
// Must be called with dl.mu held.
func (dl *DetailedLogger) rotate() {
	if dl.agentFile != nil {
		if info, err := dl.agentFile.Stat(); err == nil && info.Size() > dl.maxSize {
			path := dl.agentFile.Name()
			_ = dl.agentFile.Sync()
			_ = dl.agentFile.Close()
			rotated := strings.TrimSuffix(path, ".jsonl") + ".1.jsonl"
			_ = os.Rename(path, rotated)
			f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
			if err != nil {
				log.Printf("detailed_logging: failed to reopen agent after rotation: %v", err)
				dl.agentFile = nil
			} else {
				dl.agentFile = f
			}
		}
	}
	if dl.llmFile != nil {
		if info, err := dl.llmFile.Stat(); err == nil && info.Size() > dl.maxSize {
			path := dl.llmFile.Name()
			_ = dl.llmFile.Sync()
			_ = dl.llmFile.Close()
			rotated := strings.TrimSuffix(path, ".jsonl") + ".1.jsonl"
			_ = os.Rename(path, rotated)
			f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
			if err != nil {
				log.Printf("detailed_logging: failed to reopen llm after rotation: %v", err)
				dl.llmFile = nil
			} else {
				dl.llmFile = f
			}
		}
	}
}

// parseSize parses size strings like "50m" or "1g" into bytes.
// Returns defaultVal on empty string or parse error.
func parseSize(s string, defaultVal int64) int64 {
	if s == "" {
		return defaultVal
	}
	s = strings.ToLower(strings.TrimSpace(s))
	if strings.HasSuffix(s, "g") {
		n, err := strconv.ParseInt(s[:len(s)-1], 10, 64)
		if err != nil {
			return defaultVal
		}
		return n * 1024 * 1024 * 1024
	}
	if strings.HasSuffix(s, "m") {
		n, err := strconv.ParseInt(s[:len(s)-1], 10, 64)
		if err != nil {
			return defaultVal
		}
		return n * 1024 * 1024
	}
	return defaultVal
}
