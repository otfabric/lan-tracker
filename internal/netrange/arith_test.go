package netrange

import (
	"math/rand/v2"
	"testing"
)

// The address arithmetic is written without narrowing conversions; these
// pin its edges: /0 and /32 ranges, the last address of the space, and the
// permutation width for the smallest sets.
func TestArithmeticEdges(t *testing.T) {
	for _, tc := range []struct {
		prefix      string
		first, last string
	}{
		{"0.0.0.0/0", "0.0.0.0", "255.255.255.255"},
		{"10.1.2.3/32", "10.1.2.3", "10.1.2.3"},
		{"192.168.7.9/24", "192.168.7.0", "192.168.7.255"},
	} {
		r := Of(pfx(tc.prefix))
		if got := toAddr(r.First); got != addr(tc.first) {
			t.Errorf("Of(%s).First = %s, want %s", tc.prefix, got, tc.first)
		}
		if got := toAddr(r.Last); got != addr(tc.last) {
			t.Errorf("Of(%s).Last = %s, want %s", tc.prefix, got, tc.last)
		}
	}
	all := New(Of(pfx("0.0.0.0/0")))
	if got := all.At(all.Len() - 1); got != addr("255.255.255.255") {
		t.Errorf("At(Len-1) = %s, want 255.255.255.255", got)
	}
	if got := all.At(0); got != addr("0.0.0.0") {
		t.Errorf("At(0) = %s, want 0.0.0.0", got)
	}
	for _, tc := range []struct {
		v    uint64
		want uint32
	}{{0, 0}, {1<<32 - 1, 1<<32 - 1}, {1 << 32, 0}, {1<<32 + 5, 5}} {
		if got := low32(tc.v); got != tc.want {
			t.Errorf("low32(%#x) = %#x, want %#x", tc.v, got, tc.want)
		}
	}
	r := rand.New(rand.NewPCG(1, 2))
	for _, tc := range []struct{ n, half uint64 }{{0, 32}, {1, 1}, {2, 1}, {5, 2}, {1 << 32, 16}} {
		if p := newPermutation(r, tc.n); p.half != tc.half {
			t.Errorf("newPermutation(%d).half = %d, want %d", tc.n, p.half, tc.half)
		}
	}
}
