package main

import (
	"fmt"
	"log"
	"unicode/utf8"

	"github.com/sympozium-ai/sympozium/internal/tooloutput"
)

func toolOutputMaxBytes() int {
	maxBytes := envInt(tooloutput.MaxBytesEnv, tooloutput.DefaultMaxBytes)
	if maxBytes <= 0 {
		log.Printf("WARNING: %s must be positive; using default %d", tooloutput.MaxBytesEnv, tooloutput.DefaultMaxBytes)
		return tooloutput.DefaultMaxBytes
	}
	return maxBytes
}

func truncateAtUTF8Boundary(output string, maxBytes int) string {
	truncated := output[:maxBytes]
	for i := 0; i < utf8.UTFMax-1 && len(truncated) > 0; i++ {
		if r, size := utf8.DecodeLastRuneInString(truncated); r != utf8.RuneError || size > 1 {
			break
		}
		truncated = truncated[:len(truncated)-1]
	}
	return truncated
}

func truncateToolOutput(output string, maxBytes int, hint string) string {
	if maxBytes <= 0 || len(output) <= maxBytes {
		return output
	}
	return truncateAtUTF8Boundary(output, maxBytes) + fmt.Sprintf(
		"\n... (output truncated at %d bytes; %s)", maxBytes, hint)
}
