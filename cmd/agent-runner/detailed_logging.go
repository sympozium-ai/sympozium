package main

import (
	"bufio"
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

const maxEntryBytes = 1024 * 1024 // 1 MB per JSONL entry

var detailedLog *DetailedLogger

type DetailedLogger struct {
	agentFile *os.File
	llmFile   *os.File
	agentBuf  *bufio.Writer
	llmBuf    *bufio.Writer
	agentSize int64
	llmSize   int64
	seq       atomic.Int64
	maxSize   int64
	runID     string
	mu        sync.Mutex
}

func NewDetailedLogger(path, runID string, maxSize int64) (*DetailedLogger, error) {
	if runID != "" {
		path = filepath.Join(path, runID)
	}
	if err := os.MkdirAll(path, 0o700); err != nil {
		return nil, fmt.Errorf("creating log dir: %w", err)
	}

	agentFile, err := os.OpenFile(filepath.Join(path, "agent.jsonl"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("opening agent.jsonl: %w", err)
	}

	llmFile, err := os.OpenFile(filepath.Join(path, "llm.jsonl"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		agentFile.Close()
		return nil, fmt.Errorf("opening llm.jsonl: %w", err)
	}

	var agentSize, llmSize int64
	if info, err := agentFile.Stat(); err == nil {
		agentSize = info.Size()
	}
	if info, err := llmFile.Stat(); err == nil {
		llmSize = info.Size()
	}

	dl := &DetailedLogger{
		agentFile: agentFile,
		llmFile:   llmFile,
		agentBuf:  bufio.NewWriterSize(agentFile, 64*1024),
		llmBuf:    bufio.NewWriterSize(llmFile, 64*1024),
		agentSize: agentSize,
		llmSize:   llmSize,
		maxSize:   maxSize,
		runID:     runID,
	}

	// Seed seq from existing files so restarts don't produce duplicate seq values.
	dl.seedSeqFromFiles(path)

	return dl, nil
}

func (dl *DetailedLogger) seedSeqFromFiles(dir string) {
	var maxSeq int64
	for _, name := range []string{"agent.jsonl", "llm.jsonl"} {
		f, err := os.Open(filepath.Join(dir, name))
		if err != nil {
			continue
		}
		scanner := bufio.NewScanner(f)
		scanner.Buffer(make([]byte, 256*1024), 2*1024*1024)
		for scanner.Scan() {
			var entry map[string]any
			if json.Unmarshal(scanner.Bytes(), &entry) == nil {
				if s, ok := entry["seq"].(float64); ok && int64(s) > maxSeq {
					maxSeq = int64(s)
				}
			}
		}
		f.Close()
	}
	if maxSeq > 0 {
		dl.seq.Store(maxSeq)
	}
}

func (dl *DetailedLogger) Enabled() bool {
	return dl != nil
}

func (dl *DetailedLogger) LogAgent(event string, fields map[string]any) {
	if dl == nil {
		return
	}
	dl.writeEntry(true, event, fields)
}

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
		if s, ok := v.(string); ok && len(s) > maxEntryBytes {
			v = s[:maxEntryBytes] + "... (truncated by detailed logger)"
		}
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

	var buf *bufio.Writer
	var sizePtr *int64
	if isAgent {
		buf = dl.agentBuf
		sizePtr = &dl.agentSize
	} else {
		buf = dl.llmBuf
		sizePtr = &dl.llmSize
	}
	if buf == nil {
		return
	}

	n, err := buf.Write(data)
	if err != nil {
		log.Printf("detailed_logging: write error: %v", err)
		return
	}
	*sizePtr += int64(n)

	if *sizePtr > dl.maxSize {
		dl.rotate(isAgent)
	}
}

func (dl *DetailedLogger) Close() {
	if dl == nil {
		return
	}
	dl.mu.Lock()
	defer dl.mu.Unlock()
	if dl.agentBuf != nil {
		_ = dl.agentBuf.Flush()
	}
	if dl.agentFile != nil {
		_ = dl.agentFile.Sync()
		_ = dl.agentFile.Close()
		dl.agentFile = nil
		dl.agentBuf = nil
	}
	if dl.llmBuf != nil {
		_ = dl.llmBuf.Flush()
	}
	if dl.llmFile != nil {
		_ = dl.llmFile.Sync()
		_ = dl.llmFile.Close()
		dl.llmFile = nil
		dl.llmBuf = nil
	}
}

// rotate renames the oversize file and reopens fresh. Must be called with dl.mu held.
func (dl *DetailedLogger) rotate(isAgent bool) {
	var filePtr **os.File
	var bufPtr **bufio.Writer
	var sizePtr *int64
	if isAgent {
		filePtr = &dl.agentFile
		bufPtr = &dl.agentBuf
		sizePtr = &dl.agentSize
	} else {
		filePtr = &dl.llmFile
		bufPtr = &dl.llmBuf
		sizePtr = &dl.llmSize
	}

	f := *filePtr
	if f == nil {
		return
	}

	path := f.Name()
	if *bufPtr != nil {
		_ = (*bufPtr).Flush()
	}
	_ = f.Sync()
	_ = f.Close()

	rotated := strings.TrimSuffix(path, ".jsonl") + ".1.jsonl"
	if err := os.Rename(path, rotated); err != nil {
		log.Printf("detailed_logging: rotation rename failed: %v — disabling this stream", err)
		*filePtr = nil
		*bufPtr = nil
		return
	}

	newF, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		log.Printf("detailed_logging: failed to reopen after rotation: %v", err)
		*filePtr = nil
		*bufPtr = nil
	} else {
		*filePtr = newF
		*bufPtr = bufio.NewWriterSize(newF, 64*1024)
		*sizePtr = 0
	}
}

// parseSize parses size strings like "50m" or "1g" into bytes.
func parseSize(s string, defaultVal int64) (int64, error) {
	if s == "" {
		return defaultVal, nil
	}
	s = strings.ToLower(strings.TrimSpace(s))

	var multiplier int64
	var numStr string

	switch {
	case strings.HasSuffix(s, "g"):
		multiplier = 1024 * 1024 * 1024
		numStr = s[:len(s)-1]
	case strings.HasSuffix(s, "m"):
		multiplier = 1024 * 1024
		numStr = s[:len(s)-1]
	default:
		return 0, fmt.Errorf("unsupported size format %q: use 'm' (megabytes) or 'g' (gigabytes) suffix", s)
	}

	n, err := strconv.ParseInt(numStr, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid size %q: %w", s, err)
	}
	if n <= 0 {
		return 0, fmt.Errorf("size must be positive, got %q", s)
	}
	return n * multiplier, nil
}
