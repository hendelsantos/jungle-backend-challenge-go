// Package money provides exact monetary values with two decimal places and value semantics.
// It performs no currency conversion and has no infrastructure dependencies.
package money

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
)

const (
	BRL = "BRL"
	USD = "USD"
)

var (
	ErrInvalidMoney        = errors.New("uninitialized money")
	ErrUnsupportedCurrency = errors.New("unsupported currency")
	ErrInvalidAmount       = errors.New("invalid decimal amount")
	ErrOverflow            = errors.New("monetary overflow")
	ErrCurrencyMismatch    = errors.New("currency mismatch")
)

// Money stores signed minor units. Its zero value is invalid; use Zero instead.
// Negative values are allowed internally, but rejected by ParseDecimal and ParseJSON.
type Money struct {
	cents    int64
	currency string
}

func Zero(currency string) (Money, error) {
	return FromMinorUnits(0, currency)
}

// FromMinorUnits reconstitutes an exact internal value, including negative differences.
// Wallets must separately enforce their nonnegative balance invariant.
func FromMinorUnits(cents int64, currency string) (Money, error) {
	if err := validateCurrency(currency); err != nil {
		return Money{}, err
	}
	return Money{cents: cents, currency: currency}, nil
}

// ParseDecimal accepts unsigned ASCII decimals with zero, one or two fractional digits.
// An explicit decimal point requires at least one fractional digit. Signs, whitespace,
// exponents and rounding are not accepted. Leading zeros are normalized on output.
func ParseDecimal(amount, currency string) (Money, error) {
	if err := validateCurrency(currency); err != nil {
		return Money{}, err
	}
	whole, fraction, hasPoint := strings.Cut(amount, ".")
	if whole == "" || (hasPoint && (len(fraction) == 0 || len(fraction) > 2)) {
		return Money{}, ErrInvalidAmount
	}
	for _, part := range []string{whole, fraction} {
		for i := range len(part) {
			if part[i] < '0' || part[i] > '9' {
				return Money{}, ErrInvalidAmount
			}
		}
	}
	fraction += strings.Repeat("0", 2-len(fraction))
	var cents int64
	for _, part := range []string{whole, fraction} {
		for i := range len(part) {
			digit := int64(part[i] - '0')
			if cents > (math.MaxInt64-digit)/10 {
				return Money{}, ErrOverflow
			}
			cents = cents*10 + digit
		}
	}
	return FromMinorUnits(cents, currency)
}

// Validate rejects uninitialized values before arithmetic or serialization.
func (m Money) Validate() error {
	if m.currency == "" {
		return ErrInvalidMoney
	}
	return validateCurrency(m.currency)
}

// MinorUnits and Currency expose state without mutation. Validate a value before
// using these accessors at a boundary where uninitialized values may arrive.
func (m Money) MinorUnits() int64 { return m.cents }
func (m Money) Currency() string  { return m.currency }

func (m Money) FormatDecimal() (string, error) {
	if err := m.Validate(); err != nil {
		return "", err
	}
	var magnitude uint64
	sign := ""
	if m.cents < 0 {
		// Do not negate MinInt64: move one unit toward zero first.
		magnitude = uint64(-(m.cents + 1)) + 1
		sign = "-"
	} else {
		magnitude = uint64(m.cents)
	}
	fraction := strconv.FormatUint(magnitude%100, 10)
	if len(fraction) == 1 {
		fraction = "0" + fraction
	}
	return sign + strconv.FormatUint(magnitude/100, 10) + "." + fraction, nil
}

func (m Money) Add(other Money) (Money, error) {
	if err := m.compatibleWith(other); err != nil {
		return Money{}, err
	}
	if (other.cents > 0 && m.cents > math.MaxInt64-other.cents) ||
		(other.cents < 0 && m.cents < math.MinInt64-other.cents) {
		return Money{}, ErrOverflow
	}
	return FromMinorUnits(m.cents+other.cents, m.currency)
}

func (m Money) Sub(other Money) (Money, error) {
	if err := m.compatibleWith(other); err != nil {
		return Money{}, err
	}
	// Check subtraction directly: negating the operand would reject valid MinInt64 cases.
	if (other.cents > 0 && m.cents < math.MinInt64+other.cents) ||
		(other.cents < 0 && m.cents > math.MaxInt64+other.cents) {
		return Money{}, ErrOverflow
	}
	return FromMinorUnits(m.cents-other.cents, m.currency)
}

func (m Money) Negate() (Money, error) {
	if err := m.Validate(); err != nil {
		return Money{}, err
	}
	if m.cents == math.MinInt64 {
		return Money{}, ErrOverflow
	}
	return FromMinorUnits(-m.cents, m.currency)
}

// Compare returns -1, 0 or 1. It never subtracts operands, avoiding overflow.
func (m Money) Compare(other Money) (int, error) {
	if err := m.compatibleWith(other); err != nil {
		return 0, err
	}
	switch {
	case m.cents < other.cents:
		return -1, nil
	case m.cents > other.cents:
		return 1, nil
	default:
		return 0, nil
	}
}

func (m Money) Equal(other Money) (bool, error) {
	comparison, err := m.Compare(other)
	return comparison == 0 && err == nil, err
}

func (m Money) compatibleWith(other Money) error {
	if err := m.Validate(); err != nil {
		return err
	}
	if err := other.Validate(); err != nil {
		return err
	}
	if m.currency != other.currency {
		return ErrCurrencyMismatch
	}
	return nil
}

func validateCurrency(currency string) error {
	switch currency {
	case BRL, USD:
		return nil
	default:
		return fmt.Errorf("%w: %q", ErrUnsupportedCurrency, currency)
	}
}
