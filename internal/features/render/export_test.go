package render

import "testing"

func TestContentRectClampsAStaleRect(t *testing.T) {
	r := contentRect{X: -10, Y: 900, W: 5000, H: 500}.clamped(1000, 1000)
	if r.X != 0 || r.Y != 900 || r.W != 1000 || r.H != 100 {
		t.Fatalf("got %+v", r)
	}
}
