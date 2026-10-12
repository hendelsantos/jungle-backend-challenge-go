package money_test

import (
	"errors"
	"math"
	"math/big"
	"strings"
	"testing"

	"github.com/hendelsantos/jungle-backend-go/internal/domain/money"
)

func FuzzArithmetic(f *testing.F) {
	for _, pair := range [][2]int64{
		{0, 0}, {100, 2590}, {math.MaxInt64, 1}, {math.MinInt64, -1},
		{math.MinInt64, math.MinInt64}, {math.MinInt64, math.MaxInt64}, {-1, math.MinInt64},
	} {
		f.Add(pair[0], pair[1])
	}
	f.Fuzz(func(t *testing.T, a, b int64) {
		left, right := fromUnits(t, a, money.BRL), fromUnits(t, b, money.BRL)
		for _, subtract := range []bool{false, true} {
			want := new(big.Int)
			var actual money.Money
			var err error
			if subtract {
				want.Sub(big.NewInt(a), big.NewInt(b))
				actual, err = left.Sub(right)
			} else {
				want.Add(big.NewInt(a), big.NewInt(b))
				actual, err = left.Add(right)
			}
			if !want.IsInt64() {
				if !errors.Is(err, money.ErrOverflow) {
					t.Fatalf("a=%d b=%d subtract=%t: expected overflow, got %v", a, b, subtract, err)
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				assertValue(t, actual, want.Int64(), money.BRL)
			}
		}
	})
}

func FuzzParseDecimal(f *testing.F) {
	for _, input := range []string{"0", "1.0", "0010", "-1", "NaN", "92233720368547758.07", "92233720368547758.08", "", "1.000"} {
		f.Add(input)
	}
	f.Fuzz(func(t *testing.T, input string) {
		value, err := money.ParseDecimal(input, money.BRL)
		if err != nil {
			return
		}
		if value.MinorUnits() < 0 {
			t.Fatal("external parser accepted a negative amount")
		}
		// An exact rational is an independent oracle for every accepted decimal.
		decimal := input
		if !strings.Contains(decimal, ".") {
			// An explicit point prevents big.Rat from treating leading zeros as octal.
			decimal += ".0"
		}
		rational, ok := new(big.Rat).SetString(decimal)
		if !ok {
			t.Fatalf("accepted a nonnumeric decimal %q", input)
		}
		rational.Mul(rational, big.NewRat(100, 1))
		if !rational.IsInt() || !rational.Num().IsInt64() || rational.Num().Int64() != value.MinorUnits() {
			t.Fatalf("inexact parsing for %q", input)
		}
		text, err := value.FormatDecimal()
		if err != nil {
			t.Fatal(err)
		}
		reparsed, err := money.ParseDecimal(text, money.BRL)
		if err != nil || reparsed != value {
			t.Fatalf("normalization changed value: %q, %v", text, err)
		}
	})
}
