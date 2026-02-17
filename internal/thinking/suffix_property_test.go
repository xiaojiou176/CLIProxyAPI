package thinking

import (
	"strconv"
	"testing"
	"testing/quick"
)

func TestParseNumericSuffixProperty_NonNegativeRoundTrip(t *testing.T) {
	t.Parallel()

	err := quick.Check(func(input uint16) bool {
		raw := strconv.Itoa(int(input))
		parsed, ok := ParseNumericSuffix(raw)
		return ok && parsed == int(input)
	}, &quick.Config{MaxCount: 1000})

	if err != nil {
		t.Fatalf("property violated: %v", err)
	}
}

func TestParseNumericSuffixProperty_NegativeRejected(t *testing.T) {
	t.Parallel()

	err := quick.Check(func(input int16) bool {
		if input >= 0 {
			return true
		}
		_, ok := ParseNumericSuffix(strconv.Itoa(int(input)))
		return !ok
	}, &quick.Config{MaxCount: 1000})

	if err != nil {
		t.Fatalf("property violated: %v", err)
	}
}
