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
