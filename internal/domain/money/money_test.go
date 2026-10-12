package money_test

import (
	"errors"
	"math"
	"strings"
	"testing"

	"github.com/hendelsantos/jungle-backend-go/internal/domain/money"
)

func TestParseDecimal(t *testing.T) {
	for _, tc := range []struct {
		input, canonical string
		cents            int64
	}{
		{"0", "0.00", 0},
		{"0.00", "0.00", 0},
		{"1", "1.00", 100},
		{"1.0", "1.00", 100},
		{"1.00", "1.00", 100},
		{"25.90", "25.90", 2590},
		{"0.01", "0.01", 1},
		{"00001.2", "1.20", 120},
		{"92233720368547758.07", "92233720368547758.07", math.MaxInt64},
		{"92233720368547758", "92233720368547758.00", math.MaxInt64 - 7},
		{strings.Repeat("0", 100) + "1", "1.00", 100},
	} {
		t.Run(tc.input, func(t *testing.T) {
			value, err := money.ParseDecimal(tc.input, money.BRL)
			if err != nil {
				t.Fatal(err)
			}
			assertValue(t, value, tc.cents, money.BRL)
			formatted, err := value.FormatDecimal()
			if err != nil || formatted != tc.canonical {
				t.Fatalf("format = %q, %v; want %q", formatted, err, tc.canonical)
			}
		})
	}
}

func TestParseDecimalRejectsInvalidInput(t *testing.T) {
	for _, input := range []string{
		"", " ", " 1", "1 ", "1\n", "1\x00", "NaN", "Infinity", "-Infinity", "Inf",
		"1e2", "1E2", "-1", "-0.00", "+1", ".1", "1.", "1.000", "1.001",
		"1.2.3", "1,00", "1_000", "0x10", "１２.００", "١.٠٠", "a", ".",
	} {
		t.Run(input, func(t *testing.T) {
			_, err := money.ParseDecimal(input, money.BRL)
			if !errors.Is(err, money.ErrInvalidAmount) {
				t.Fatalf("error = %v, want invalid amount", err)
			}
		})
	}
	for _, input := range []string{"92233720368547758.08", "92233720368547759", "9223372036854775807", strings.Repeat("9", 200)} {
		if _, err := money.ParseDecimal(input, money.BRL); !errors.Is(err, money.ErrOverflow) {
			t.Fatalf("ParseDecimal(%q): %v, want overflow", input, err)
		}
	}
}

func TestCurrenciesAndZero(t *testing.T) {
	for _, currency := range []string{money.BRL, money.USD} {
		value, err := money.Zero(currency)
		if err != nil {
			t.Fatal(err)
		}
		assertValue(t, value, 0, currency)
		value, err = money.ParseDecimal("1.23", currency)
		if err != nil {
			t.Fatal(err)
		}
		assertValue(t, value, 123, currency)
	}
	for _, currency := range []string{"", "brl", "BRL ", " BRL", "EUR", "BTC", "XXX"} {
		_, zeroErr := money.Zero(currency)
		_, minorErr := money.FromMinorUnits(1, currency)
		_, parseErr := money.ParseDecimal("1", currency)
		for _, err := range []error{zeroErr, minorErr, parseErr} {
			if !errors.Is(err, money.ErrUnsupportedCurrency) {
				t.Fatalf("currency %q: error = %v", currency, err)
			}
		}
	}
}

func TestArithmetic(t *testing.T) {
	for _, tc := range []struct {
		name               string
		a, b, want         int64
		subtract, overflow bool
	}{
		{"add", 100, 2590, 2690, false, false},
		{"subtract", 2590, 100, 2490, true, false},
		{"negative difference", 100, 2590, -2490, true, false},
		{"add negative", 100, -200, -100, false, false},
		{"subtract negative", 100, -200, 300, true, false},
		{"add max boundary", math.MaxInt64 - 1, 1, math.MaxInt64, false, false},
		{"add min boundary", math.MinInt64 + 1, -1, math.MinInt64, false, false},
		{"add overflow", math.MaxInt64, 1, 0, false, true},
		{"add underflow", math.MinInt64, -1, 0, false, true},
		{"add opposite extremes", math.MaxInt64, math.MinInt64, -1, false, false},
		{"subtract underflow", math.MinInt64, 1, 0, true, true},
		{"subtract overflow", math.MaxInt64, -1, 0, true, true},
		{"subtract min from zero", 0, math.MinInt64, 0, true, true},
		{"subtract min from min", math.MinInt64, math.MinInt64, 0, true, false},
		{"subtract min from negative one", -1, math.MinInt64, math.MaxInt64, true, false},
		{"subtract max from negative one", -1, math.MaxInt64, math.MinInt64, true, false},
		{"subtract opposite extremes", math.MinInt64, math.MaxInt64, 0, true, true},
		{"add zero", math.MinInt64, 0, math.MinInt64, false, false},
		{"subtract zero", math.MaxInt64, 0, math.MaxInt64, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			left, right := fromUnits(t, tc.a, money.BRL), fromUnits(t, tc.b, money.BRL)
			originalLeft, originalRight := left, right
			operation := left.Add
			if tc.subtract {
				operation = left.Sub
			}
			value, err := operation(right)
			if tc.overflow {
				if !errors.Is(err, money.ErrOverflow) {
					t.Fatalf("error = %v, want overflow", err)
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				assertValue(t, value, tc.want, money.BRL)
			}
			if left != originalLeft || right != originalRight {
				t.Fatal("arithmetic mutated its inputs")
			}
		})
	}
}

func TestNegateAndSignedFormatting(t *testing.T) {
	for _, tc := range []struct {
		cents int64
		text  string
	}{
		{0, "0.00"}, {1, "0.01"}, {-1, "-0.01"}, {-99, "-0.99"},
		{-100, "-1.00"}, {-2590, "-25.90"},
		{math.MaxInt64, "92233720368547758.07"},
		{math.MinInt64, "-92233720368547758.08"},
	} {
		value := fromUnits(t, tc.cents, money.BRL)
		text, err := value.FormatDecimal()
		if err != nil || text != tc.text {
			t.Fatalf("FormatDecimal(%d) = %q, %v", tc.cents, text, err)
		}
		negated, err := value.Negate()
		if tc.cents == math.MinInt64 {
			if !errors.Is(err, money.ErrOverflow) {
				t.Fatalf("negation error = %v, want overflow", err)
			}
		} else {
			if err != nil {
				t.Fatal(err)
			}
			assertValue(t, negated, -tc.cents, money.BRL)
		}
		assertValue(t, value, tc.cents, money.BRL)
	}
}

func TestCompareAndEqual(t *testing.T) {
	for _, tc := range []struct {
		a, b       int64
		comparison int
	}{
		{0, 0, 0}, {100, 100, 0}, {1, 2, -1}, {2, 1, 1},
		{math.MinInt64, math.MaxInt64, -1}, {math.MaxInt64, math.MinInt64, 1},
	} {
		left, right := fromUnits(t, tc.a, money.BRL), fromUnits(t, tc.b, money.BRL)
		comparison, err := left.Compare(right)
		if err != nil || comparison != tc.comparison {
			t.Fatalf("Compare(%d, %d) = %d, %v", tc.a, tc.b, comparison, err)
		}
		equal, err := left.Equal(right)
		if err != nil || equal != (tc.comparison == 0) {
			t.Fatalf("Equal(%d, %d) = %t, %v", tc.a, tc.b, equal, err)
		}
	}
}

func TestOperationsRejectInvalidAndIncompatibleValues(t *testing.T) {
	brl, usd := fromUnits(t, 100, money.BRL), fromUnits(t, 100, money.USD)
	for _, tc := range []struct {
		left, right money.Money
		want        error
	}{
		{brl, usd, money.ErrCurrencyMismatch},
		{usd, brl, money.ErrCurrencyMismatch},
		{money.Money{}, brl, money.ErrInvalidMoney},
		{brl, money.Money{}, money.ErrInvalidMoney},
		{money.Money{}, money.Money{}, money.ErrInvalidMoney},
	} {
		_, addErr := tc.left.Add(tc.right)
		_, subErr := tc.left.Sub(tc.right)
		_, cmpErr := tc.left.Compare(tc.right)
		equal, equalErr := tc.left.Equal(tc.right)
		if equal {
			t.Fatal("invalid or incompatible values must not compare equal")
		}
		for _, err := range []error{addErr, subErr, cmpErr, equalErr} {
			if !errors.Is(err, tc.want) {
				t.Fatalf("error = %v, want %v", err, tc.want)
			}
		}
	}
	var invalid money.Money
	_, formatErr := invalid.FormatDecimal()
	_, negateErr := invalid.Negate()
	for _, err := range []error{invalid.Validate(), formatErr, negateErr} {
		if !errors.Is(err, money.ErrInvalidMoney) {
			t.Fatalf("error = %v, want invalid money", err)
		}
	}
}

func fromUnits(t testing.TB, cents int64, currency string) money.Money {
	t.Helper()
	value, err := money.FromMinorUnits(cents, currency)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func assertValue(t testing.TB, value money.Money, cents int64, currency string) {
	t.Helper()
	if err := value.Validate(); err != nil {
		t.Fatal(err)
	}
	if value.MinorUnits() != cents || value.Currency() != currency {
		t.Fatalf("money = %d %s, want %d %s", value.MinorUnits(), value.Currency(), cents, currency)
	}
}
