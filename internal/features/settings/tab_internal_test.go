package settings

import (
	"errors"
	"fmt"
	"testing"
)

func TestRootCause(t *testing.T) {
	inner := errors.New("line 3: expected a number\n\n")
	if got := rootCause(fmt.Errorf("parse config.toml: %w", inner)); got != "line 3: expected a number" {
		t.Fatalf("got %q", got)
	}
}
