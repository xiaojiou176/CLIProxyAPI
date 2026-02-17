package thinking

import (
	"strings"
	"testing"
)

func FuzzParseSuffix(f *testing.F) {
	seeds := []string{
		"",
		"gpt-5.2",
		"gpt-5.2(high)",
		"claude-sonnet-4-5(16384)",
		"model(",
		"model)",
		"model(()",
		"model(xhigh)",
	}
	for _, seed := range seeds {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, model string) {
		result := ParseSuffix(model)

		if result.HasSuffix {
			if !strings.HasSuffix(model, ")") {
				t.Fatalf("suffix marked true but model does not end with ')': %q", model)
			}
			reconstructed := result.ModelName + "(" + result.RawSuffix + ")"
			if reconstructed != model {
				t.Fatalf("round trip mismatch: model=%q reconstructed=%q", model, reconstructed)
			}
		}

		if budget, ok := ParseNumericSuffix(result.RawSuffix); ok && budget < 0 {
			t.Fatalf("negative budget should be rejected: model=%q budget=%d", model, budget)
		}

		if mode, ok := ParseSpecialSuffix(result.RawSuffix); ok && mode == ModeBudget {
			t.Fatalf("special suffix cannot resolve to ModeBudget: model=%q raw=%q", model, result.RawSuffix)
		}

		if level, ok := ParseLevelSuffix(result.RawSuffix); ok {
			switch level {
			case LevelMinimal, LevelLow, LevelMedium, LevelHigh, LevelXHigh:
			default:
				t.Fatalf("unexpected level parsed: %q", level)
			}
		}
	})
}
