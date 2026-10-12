package money_test

import (
	"encoding/json"
	"errors"
	"math"
	"testing"

	"github.com/hendelsantos/jungle-backend-go/internal/domain/money"
)

func TestJSONContract(t *testing.T) {
	for _, input := range []string{
		`{"amount":"1","currency":"BRL"}`,
		`{"currency":"BRL","amount":"1.0"}`,
		` {"amount":"1.00","currency":"BRL"} `,
		`{"amount":"0001.00","currency":"BRL"}`,
	} {
		value, err := money.ParseJSON([]byte(input))
		if err != nil {
			t.Fatal(err)
		}
		assertValue(t, value, 100, money.BRL)
		encoded, err := json.Marshal(value)
		if err != nil || string(encoded) != `{"amount":"1.00","currency":"BRL"}` {
			t.Fatalf("encoded = %s, %v", encoded, err)
		}
	}
	for _, currency := range []string{money.BRL, money.USD} {
		for _, cents := range []int64{0, 1, math.MaxInt64} {
			value := fromUnits(t, cents, currency)
			encoded, err := json.Marshal(value)
			if err != nil {
				t.Fatal(err)
			}
			decoded, err := money.ParseJSON(encoded)
			if err != nil {
				t.Fatal(err)
			}
			assertValue(t, decoded, cents, currency)
		}
	}
}

func TestJSONRejectsInvalidInput(t *testing.T) {
	for _, input := range []string{
		``, `null`, `{}`, `[]`, `"1.00"`, `1`, `{`,
		`{"amount":1.00,"currency":"BRL"}`,
		`{"amount":92233720368547758.07,"currency":"BRL"}`,
		`{"amount":null,"currency":"BRL"}`,
		`{"amount":true,"currency":"BRL"}`,
		`{"currency":"BRL"}`,
		`{"amount":"1.00"}`,
		`{"amount":"1.00","currency":null}`,
		`{"amount":"1.00","currency":1}`,
		`{"amount":"1.00","currency":"EUR"}`,
		`{"amount":"-0.01","currency":"BRL"}`,
		`{"amount":"1.001","currency":"BRL"}`,
		`{"amount":"1e2","currency":"BRL"}`,
		`{"amount":"92233720368547758.08","currency":"BRL"}`,
		`{"amount":"1.00","currency":"BRL","extra":true}`,
		`{"amount":"1.00","currency":"BRL"} {}`,
		`{"amount":"1.00","currency":"BRL"} null`,
		`{"amount":"1.00","currency":"BRL"} garbage`,
	} {
		t.Run(input, func(t *testing.T) {
			value, err := money.ParseJSON([]byte(input))
			if err == nil || value.Validate() == nil {
				t.Fatalf("invalid input accepted: %+v, %v", value, err)
			}
		})
	}
	if _, err := money.ParseJSON([]byte(`{"amount":1,"currency":"BRL"}`)); !errors.Is(err, money.ErrInvalidJSON) {
		t.Fatalf("numeric amount error = %v", err)
	}
}

func TestJSONSignedOutputAndInvalidZeroValue(t *testing.T) {
	value := fromUnits(t, math.MinInt64, money.BRL)
	encoded, err := json.Marshal(value)
	if err != nil || string(encoded) != `{"amount":"-92233720368547758.08","currency":"BRL"}` {
		t.Fatalf("signed JSON = %s, %v", encoded, err)
	}
	if _, err := money.ParseJSON(encoded); !errors.Is(err, money.ErrInvalidAmount) {
		t.Fatalf("external input must reject signed output: %v", err)
	}
	if _, err := json.Marshal(money.Money{}); !errors.Is(err, money.ErrInvalidMoney) {
		t.Fatalf("uninitialized money serialization: %v", err)
	}
}

func TestJSONDecodingReplacesOnlyAfterValidation(t *testing.T) {
	value := fromUnits(t, 2500, money.BRL)
	copy := value
	for _, input := range []string{
		`{"amount":1,"currency":"BRL"}`,
		`{"amount":"-1","currency":"BRL"}`,
		`{"amount":"92233720368547758.08","currency":"BRL"}`,
		`null`,
	} {
		if err := json.Unmarshal([]byte(input), &value); err == nil {
			t.Fatalf("json.Unmarshal accepted %s", input)
		}
		assertValue(t, value, 2500, money.BRL)
	}
	if err := json.Unmarshal([]byte(`{"amount":"1.2","currency":"USD"}`), &value); err != nil {
		t.Fatal(err)
	}
	assertValue(t, value, 120, money.USD)
	assertValue(t, copy, 2500, money.BRL)
	var nilReceiver *money.Money
	if err := nilReceiver.UnmarshalJSON([]byte(`{}`)); !errors.Is(err, money.ErrInvalidMoney) {
		t.Fatalf("nil receiver error = %v", err)
	}
}
