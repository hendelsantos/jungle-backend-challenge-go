package jsonx

import (
	"encoding/json"
	"strings"
	"testing"
)

type moneyJSON struct {
	Amount   string `json:"amount"`
	Currency string `json:"currency"`
}

type wagerJSON struct {
	ProviderID string    `json:"providerId"`
	Kind       string    `json:"kind"`
	Money      moneyJSON `json:"money"`
	Reference  string    `json:"referenceExternalTransactionId,omitempty"`
}

func TestDecodeAcceptsStrictContract(t *testing.T) {
	var payload wagerJSON
	if err := Decode([]byte(`{"providerId":"provider-a","kind":"BET","money":{"amount":"25.00","currency":"BRL"}}`), &payload); err != nil {
		t.Fatalf("valid document rejected: %v", err)
	}
	if payload.Money.Amount != "25.00" || payload.Kind != "BET" {
		t.Fatalf("payload = %+v", payload)
	}
}

func TestDecodeRejectsInvalidDocuments(t *testing.T) {
	valid := `{"providerId":"provider-a","kind":"BET","money":{"amount":"25.00","currency":"BRL"}}`
	for _, tc := range []struct {
		name, body string
	}{
		{"number amount", strings.Replace(valid, `"amount":"25.00"`, `"amount":25.00`, 1)},
		{"null kind", strings.Replace(valid, `"kind":"BET"`, `"kind":null`, 1)},
		{"boolean", strings.Replace(valid, `"kind":"BET"`, `"kind":true`, 1)},
		{"duplicate key", strings.Replace(valid, `"kind":"BET"`, `"kind":"BET","kind":"WIN"`, 1)},
		{"duplicate key case-insensitive", strings.Replace(valid, `"kind":"BET"`, `"kind":"BET","Kind":"WIN"`, 1)},
		{"unknown field", strings.Replace(valid, `"kind":"BET"`, `"kind":"BET","extra":"x"`, 1)},
		{"trailing content", valid + " {}"},
		{"top-level array", `[{"providerId":"a"}]`},
		{"top-level null", `null`},
		{"depth limit", `{"a":{"b":{"c":{"d":{"e":{"f":{"g":{"h":{"i":"1"}}}}}}}}}`},
		{"invalid utf8", "{\"providerId\":\"\xff\"}"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var payload wagerJSON
			if err := Decode([]byte(tc.body), &payload); err != ErrInvalid {
				t.Fatalf("err = %v, want ErrInvalid", err)
			}
		})
	}
}

func TestDecodePreservesDestinationOnFailure(t *testing.T) {
	payload := wagerJSON{Kind: "BET"}
	if err := Decode([]byte(`{"providerId":"a","kind":1}`), &payload); err == nil {
		t.Fatal("expected rejection")
	}
	if payload.Kind != "BET" {
		t.Fatalf("destination changed on failure: %+v", payload)
	}
}

func TestDecodeMoneyRoundTripStaysString(t *testing.T) {
	encoded, err := json.Marshal(moneyJSON{Amount: "25.00", Currency: "BRL"})
	if err != nil {
		t.Fatal(err)
	}
	var decoded moneyJSON
	if err := Decode(encoded, &decoded); err != nil || decoded.Amount != "25.00" {
		t.Fatalf("round trip: %+v %v", decoded, err)
	}
}
