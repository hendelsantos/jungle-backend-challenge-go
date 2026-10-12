package money

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

var ErrInvalidJSON = errors.New("invalid money JSON")

type representation struct {
	Amount   string `json:"amount"`
	Currency string `json:"currency"`
}

// MarshalJSON also supports signed internal differences, e.g. reconciliation output.
func (m Money) MarshalJSON() ([]byte, error) {
	amount, err := m.FormatDecimal()
	if err != nil {
		return nil, err
	}
	return json.Marshal(representation{Amount: amount, Currency: m.currency})
}

// UnmarshalJSON supports construction through encoding/json. It replaces the
// destination only after validation, preserving the previous value on failure.
func (m *Money) UnmarshalJSON(data []byte) error {
	if m == nil {
		return ErrInvalidMoney
	}
	value, err := ParseJSON(data)
	if err != nil {
		return err
	}
	*m = value
	return nil
}

// ParseJSON constructs a new external value without mutating an existing Money.
// Amount must be a nonnegative decimal string. Signed internal values must be
// rehydrated with FromMinorUnits, not through the external input contract.
func ParseJSON(data []byte) (Money, error) {
	var value representation
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&value); err != nil {
		return Money{}, fmt.Errorf("%w: %w", ErrInvalidJSON, err)
	}
	if err := decoder.Decode(new(json.RawMessage)); !errors.Is(err, io.EOF) {
		return Money{}, ErrInvalidJSON
	}
	return ParseDecimal(value.Amount, value.Currency)
}
