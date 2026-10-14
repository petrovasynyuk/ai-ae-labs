package main

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

func validRawToolCall() RawToolCall {
	return RawToolCall{
		Name:      ToolNameGetExchangeRate,
		Arguments: json.RawMessage(`{"base":"USD","target":"UAH"}`),
	}
}

func TestRunWithRepair(t *testing.T) {
	tests := []struct {
		name string
		call RawToolCall
	}{
		{
			name: "repairs missing base",
			call: RawToolCall{
				Name:      ToolNameGetExchangeRate,
				Arguments: json.RawMessage(`{"target":"UAH"}`),
			},
		},
		{
			name: "repairs wrong argument type",
			call: RawToolCall{
				Name:      ToolNameGetExchangeRate,
				Arguments: json.RawMessage(`{"base":123,"target":"UAH"}`),
			},
		},
		{
			name: "repairs unknown tool name",
			call: RawToolCall{
				Name:      ToolName("other_tool"),
				Arguments: json.RawMessage(`{"base":"USD","target":"UAH"}`),
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			repairs := 0
			repairer := RepairFunc(func(context.Context, RawToolCall, error) (RawToolCall, error) {
				repairs++
				return validRawToolCall(), nil
			})

			got, err := RunWithRepair(
				context.Background(),
				fixture(),
				tc.call,
				repairer,
				RepairConfig{MaxAttempts: 1},
			)
			if err != nil {
				t.Fatalf("RunWithRepair() error = %v", err)
			}
			if got.Rate != 41.5 {
				t.Errorf("Rate = %v, want 41.5", got.Rate)
			}
			if repairs != 1 {
				t.Errorf("repair calls = %d, want 1", repairs)
			}
		})
	}
}

func TestRunWithRepairStopsAtLimit(t *testing.T) {
	repairs := 0
	repairer := RepairFunc(func(_ context.Context, call RawToolCall, _ error) (RawToolCall, error) {
		repairs++
		return call, nil
	})

	_, err := RunWithRepair(
		context.Background(),
		fixture(),
		RawToolCall{
			Name:      ToolNameGetExchangeRate,
			Arguments: json.RawMessage(`{"base":"US1","target":"UAH"}`),
		},
		repairer,
		RepairConfig{MaxAttempts: 2},
	)
	if !errors.Is(err, ErrRepairExhausted) {
		t.Fatalf("error = %v, want ErrRepairExhausted", err)
	}
	if repairs != 2 {
		t.Errorf("repair calls = %d, want 2", repairs)
	}
}
