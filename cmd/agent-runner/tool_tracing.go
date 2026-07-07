package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
)

func executeToolCallWithTelemetry(ctx context.Context, name, argsJSON, callID string) string {
	start := time.Now()
	toolCtx, toolSpan := obs.startToolSpan(ctx, name, callID)

	var result string
	if name == ToolExecuteCommand {
		skillStart := time.Now()
		skillCtx, skillSpan := obs.startSkillSpan(toolCtx,
			"command-executor",
			"tool-executor",
			"namespace",
		)
		result = executeToolCall(skillCtx, name, argsJSON)
		if strings.HasPrefix(result, "Error:") {
			err := fmt.Errorf("%s", result)
			markSpanError(skillSpan, err)
		} else {
			skillSpan.SetStatus(codes.Ok, "")
		}
		skillSpan.SetAttributes(attribute.Int64("duration_ms", time.Since(skillStart).Milliseconds()))
		skillSpan.End()
		obs.recordSkillDuration(toolCtx, "command-executor", time.Since(skillStart))
	} else {
		result = executeToolCall(toolCtx, name, argsJSON)
	}

	if strings.HasPrefix(result, "Error:") {
		err := fmt.Errorf("%s", result)
		markSpanError(toolSpan, err)
		obs.recordToolInvocation(toolCtx, name, "error")
	} else {
		toolSpan.SetStatus(codes.Ok, "")
		obs.recordToolInvocation(toolCtx, name, "success")
	}
	toolSpan.SetAttributes(attribute.Int64("duration_ms", time.Since(start).Milliseconds()))
	toolSpan.End()
	return result
}
