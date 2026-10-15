package sqs_test

import (
	"testing"

	sqsadapter "github.com/hendelsantos/jungle-backend-challenge-go/internal/adapters/sqs"
)

func TestConfigValidateEndpointSemantics(t *testing.T) {
	cases := []struct {
		name      string
		config    sqsadapter.Config
		wantError bool
	}{
		// An empty endpoint selects the managed AWS service of the region
		// (homologation/production); the SDK resolves the regional URL.
		{"managed AWS service", sqsadapter.Config{Region: "us-east-1"}, false},
		{"local broker", sqsadapter.Config{EndpointURL: "http://127.0.0.1:4566", Region: "us-east-1"}, false},
		{"missing region", sqsadapter.Config{EndpointURL: "http://127.0.0.1:4566"}, true},
		{"missing region managed", sqsadapter.Config{}, true},
		{"malformed endpoint", sqsadapter.Config{EndpointURL: "not-a-url", Region: "us-east-1"}, true},
		{"endpoint with credentials", sqsadapter.Config{EndpointURL: "http://a:b@127.0.0.1:4566", Region: "us-east-1"}, true},
		{"endpoint with query", sqsadapter.Config{EndpointURL: "http://127.0.0.1:4566?x=1", Region: "us-east-1"}, true},
		{"prefix too long", sqsadapter.Config{EndpointURL: "http://127.0.0.1:4566", Region: "us-east-1", QueuePrefix: "012345678901234567890123456789012"}, true},
		{"prefix invalid char", sqsadapter.Config{EndpointURL: "http://127.0.0.1:4566", Region: "us-east-1", QueuePrefix: "bad.prefix-"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.config.Validate(); (err != nil) != tc.wantError {
				t.Fatalf("Validate(%+v) = %v, wantError=%v", tc.config, err, tc.wantError)
			}
		})
	}
}
