package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
)

var ErrRepairExhausted = errors.New("repair attempts exhausted")

type ToolName string

const ToolNameGetExchangeRate ToolName = "get_exchange_rate"

type RawToolCall struct {
	Name      ToolName        `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

type RepairConfig struct {
	// MaxAttempts is the maximum number of repair calls after the first failure.
	// Zero permits no repair attempt.
	MaxAttempts int
}

type Repairer interface {
	Repair(ctx context.Context, call RawToolCall, cause error) (RawToolCall, error)
}

type RepairFunc func(context.Context, RawToolCall, error) (RawToolCall, error)

func (f RepairFunc) Repair(ctx context.Context, call RawToolCall, cause error) (RawToolCall, error) {
	return f(ctx, call, cause)
}

func runRawToolCall(ctx context.Context, p Provider, call RawToolCall) (RateOutput, error) {
	if call.Name != ToolNameGetExchangeRate {
		return RateOutput{}, fmt.Errorf("unknown tool name %q", call.Name)
	}

	var in RateInput
	if err := json.Unmarshal(call.Arguments, &in); err != nil {
		return RateOutput{}, fmt.Errorf("decode tool arguments: %w", err)
	}

	return Convert(ctx, p, in)
}

func RunWithRepair(
	ctx context.Context,
	p Provider,
	initial RawToolCall,
	repairer Repairer,
	cfg RepairConfig,
) (RateOutput, error) {
	if cfg.MaxAttempts < 0 {
		return RateOutput{}, fmt.Errorf("max repair attempts must not be negative")
	}

	call := initial
	for attempt := 0; ; attempt++ {
		out, err := runRawToolCall(ctx, p, call)
		if err == nil {
			return out, nil
		}
		if attempt == cfg.MaxAttempts {
			return RateOutput{}, fmt.Errorf(
				"%w after %d attempts: %w",
				ErrRepairExhausted,
				attempt,
				err,
			)
		}
		if repairer == nil {
			return RateOutput{}, fmt.Errorf("repairer is nil: %w", err)
		}

		call, err = repairer.Repair(ctx, call, err)
		if err != nil {
			return RateOutput{}, fmt.Errorf("repair attempt %d: %w", attempt+1, err)
		}
	}
}
