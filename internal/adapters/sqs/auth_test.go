package sqs

import (
	"github.com/aws/aws-sdk-go-v2/aws"
	"testing"
)

func TestProducerSignatureBindsProviderAndExactBody(t *testing.T) {
	keys := ProviderKeys{"provider-a": "test-signing-key-a-at-least-32-bytes", "provider-b": "test-signing-key-b-at-least-32-bytes"}
	body := []byte(validEnvelope)
	attrs := SignAttributes("provider-a", keys["provider-a"], body)
	if provider, err := keys.Authenticate(body, attrs); err != nil || provider != "provider-a" {
		t.Fatalf("%q %v", provider, err)
	}
	if _, err := keys.Authenticate(append(body, ' '), attrs); err == nil {
		t.Fatal("rewritten body accepted")
	}
	changed := attrs["providerId"]
	changed.StringValue = aws.String("provider-b")
	attrs["providerId"] = changed
	if _, err := keys.Authenticate(body, attrs); err == nil {
		t.Fatal("forged provider accepted")
	}
	if _, err := keys.Authenticate(body, nil); err == nil {
		t.Fatal("unsigned producer accepted")
	}
}

func TestProducerAuthenticationRejectsUnknownOrMalformedSignature(t *testing.T) {
	keys := ProviderKeys{"provider-a": "test-signing-key-a-at-least-32-bytes"}
	for _, signature := range []string{"", "invalid", "00", "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"} {
		t.Run(signature, func(t *testing.T) {
			attrs := SignAttributes("provider-a", keys["provider-a"], []byte(validEnvelope))
			value := attrs["signature"]
			value.StringValue = aws.String(signature)
			attrs["signature"] = value
			if _, err := keys.Authenticate([]byte(validEnvelope), attrs); err == nil {
				t.Fatal("malformed/forged signature accepted")
			}
		})
	}
	attrs := SignAttributes("unknown", "test-signing-key-a-at-least-32-bytes", []byte(validEnvelope))
	if _, err := keys.Authenticate([]byte(validEnvelope), attrs); err == nil {
		t.Fatal("unknown provider accepted")
	}
}
