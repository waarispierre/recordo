package telemetry

import (
	"encoding/json"
	"testing"
)

func TestReadsTheRustFormat(t *testing.T) {
	raw := `{"events":[{"t_ns":5,"kind":"down","x":1.5,"y":2}],"tap_installed":true}`
	var tel Telemetry
	if err := json.Unmarshal([]byte(raw), &tel); err != nil {
		t.Fatal(err)
	}
	if !tel.TapInstalled || tel.Count(Down) != 1 || tel.Events[0].X != 1.5 {
		t.Fatalf("unexpected decode: %+v", tel)
	}
}
