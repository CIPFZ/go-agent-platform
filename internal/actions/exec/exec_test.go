package exec

import "testing"

func TestOutputLimitClamp(t *testing.T) {
	cases := map[int64]int64{0: DefaultOutputLimit, -1: DefaultOutputLimit, 10: 10, MaxOutputLimit + 1: MaxOutputLimit}
	for in, want := range cases {
		if got := OutputLimit(in); got != want {
			t.Errorf("OutputLimit(%d) = %d, want %d", in, got, want)
		}
	}
}

func TestDecodeOutputReplacesInvalidUTF8(t *testing.T) {
	if got := DecodeOutput("sh", []byte("ok\xffx")); got != "ok�x" {
		t.Fatalf("got %q", got)
	}
	if got := DecodeOutput("cmd", []byte("héllo")); got != "héllo" {
		t.Fatalf("valid UTF-8 changed: %q", got)
	}
}
