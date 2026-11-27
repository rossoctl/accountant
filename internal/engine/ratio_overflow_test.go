package engine

import (
	"math"
	"math/big"
	"testing"
)

// refMulDiv is a*b/c in big integers, truncated toward zero, clamped to int. It is the contract of mulDiv.
func refMulDiv(a, b, c int) int {
	q := new(big.Int).Mul(big.NewInt(int64(a)), big.NewInt(int64(b)))
	q.Quo(q, big.NewInt(int64(c)))
	if q.Cmp(big.NewInt(math.MaxInt)) > 0 {
		return math.MaxInt
	}
	if q.Cmp(big.NewInt(math.MinInt)) < 0 {
		return math.MinInt
	}
	return int(q.Int64())
}

// mulDiv equals the big-integer reference on every platform: the fast path, the wide path above 2^63, negative
// operands, and the clamp. Values that do not fit the platform int are left out.
func TestMulDivMatchesBigReference(t *testing.T) {
	var vals []int
	for _, v := range []int64{0, 1, 2, 3, 7, 250, 1000, 123456789, math.MaxInt32 - 1, math.MaxInt32, math.MaxInt32 + 1,
		3_000_000_000, 5_000_000_000, 1 << 40, 1 << 62, math.MaxInt64, -1, -7, -250, -math.MaxInt32, -3_000_000_000,
		-(1 << 62), math.MinInt64, math.MinInt32} {
		if int64(int(v)) == v {
			vals = append(vals, int(v))
		}
	}
	for _, a := range vals {
		for _, b := range vals {
			for _, c := range vals {
				if c <= 0 {
					continue
				}
				if got, want := mulDiv(a, b, c), refMulDiv(a, b, c); got != want {
					t.Fatalf("mulDiv(%d,%d,%d) = %d, want %d", a, b, c, got, want)
				}
			}
		}
	}
	// The values of the old int64 formula are the same where the product does not wrap.
	if got := mulDiv(-7, 3, 2); got != -10 {
		t.Fatalf("mulDiv(-7,3,2) = %d, want -10 (truncated toward zero)", got)
	}
}

func TestAddSatClamps(t *testing.T) {
	if got := addSat(math.MaxInt, 1); got != math.MaxInt {
		t.Fatalf("addSat = %d, want MaxInt", got)
	}
	if got := addSat(math.MinInt, -1); got != math.MinInt {
		t.Fatalf("addSat = %d, want MinInt", got)
	}
}

// The clamp holds on a 32-bit int too: a quotient above int is MaxInt, never a truncated value.
func TestRatioTokensClampsToInt(t *testing.T) {
	got := RatioFromText(math.MaxInt32, 1, false).Tokens(math.MaxInt32)
	if want := refMulDiv(math.MaxInt32, math.MaxInt32, 1); got != want {
		t.Fatalf("Tokens = %d, want %d (the true value, clamped to int)", got, want)
	}
	if math.MaxInt == math.MaxInt32 && got != math.MaxInt {
		t.Fatalf("on a 32-bit int, Tokens = %d, want MaxInt", got)
	}
}
