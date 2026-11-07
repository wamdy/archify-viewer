package desktop

import "testing"

func TestParseCloseBehavior(t *testing.T) {
	cases := map[string]CloseBehavior{
		"ask":   CloseAsk,
		"tray":  CloseTray,
		"quit":  CloseQuit,
		"":      CloseTray,
		"bogus": CloseTray,
	}
	for input, want := range cases {
		if got := ParseCloseBehavior(input); got != want {
			t.Fatalf("ParseCloseBehavior(%q) = %d, want %d", input, got, want)
		}
	}
}
