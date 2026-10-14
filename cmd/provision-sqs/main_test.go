package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestProvisionConfigurationAndDenied(t *testing.T) {
	for _, endpoint := range []string{"", "not-a-url", "http://user:password@localhost:4566", "http://localhost:4566?secret=redacted"} {
		t.Setenv("SQS_ENDPOINT_URL", endpoint)
		t.Setenv("SQS_REGION", "us-east-1")
		if err := run(t.Context()); err == nil {
			t.Fatal("invalid config accepted")
		}
	}
	t.Setenv("SQS_PROVISIONER_PROFILE", "")
	t.Setenv("AWS_ACCESS_KEY_ID", "fixture")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "fixture-not-a-real-secret")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.Header.Get("Authorization"), "/us-east-1/sqs/aws4_request") {
			t.Error("expected explicit or default us-east-1 signing region")
		}
		w.Header().Set("Content-Type", "application/x-amz-json-1.0")
		w.WriteHeader(403)
		fmt.Fprint(w, `{"__type":"AccessDenied","message":"provisioner lacks permission"}`)
	}))
	defer server.Close()
	t.Setenv("SQS_ENDPOINT_URL", server.URL)
	for _, region := range []string{"", "us-east-1"} {
		t.Setenv("SQS_REGION", region)
		err := run(t.Context())
		if err == nil || !strings.Contains(err.Error(), "AccessDenied") || strings.Contains(err.Error(), "fixture-not-a-real-secret") {
			t.Fatalf("unsafe/unclear error: %v", err)
		}
	}
}
