package sqs_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/sqs"
	sqsadapter "github.com/hendelsantos/jungle-backend-challenge-go/internal/adapters/sqs"
)

// Isolate the credential chain without reading the developer's AWS files or
// contacting instance metadata. Setenv restores each value at test cleanup;
// these tests must not run in parallel because environment is process-wide.
func isolatedCredentials(t *testing.T) string {
	t.Helper()
	for _, name := range []string{
		"AWS_ACCESS_KEY_ID", "AWS_ACCESS_KEY", "AWS_SECRET_ACCESS_KEY", "AWS_SECRET_KEY",
		"AWS_SESSION_TOKEN", "AWS_PROFILE", "AWS_DEFAULT_PROFILE",
		"AWS_WEB_IDENTITY_TOKEN_FILE", "AWS_ROLE_ARN", "AWS_ROLE_SESSION_NAME",
		"AWS_CONTAINER_CREDENTIALS_FULL_URI", "AWS_CONTAINER_CREDENTIALS_RELATIVE_URI",
		"AWS_CONTAINER_AUTHORIZATION_TOKEN", "AWS_CONTAINER_AUTHORIZATION_TOKEN_FILE",
	} {
		t.Setenv(name, "")
	}
	t.Setenv("AWS_EC2_METADATA_DISABLED", "true")
	dir := t.TempDir()
	for _, file := range []string{"credentials", "config"} {
		if err := os.WriteFile(filepath.Join(dir, file), nil, 0600); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", filepath.Join(dir, "credentials"))
	t.Setenv("AWS_CONFIG_FILE", filepath.Join(dir, "config"))
	return filepath.Join(dir, "credentials")
}

func TestSQSClientCredentialChain(t *testing.T) {
	for _, source := range []string{"environment", "shared profile", "container role"} {
		t.Run(source, func(t *testing.T) {
			file := isolatedCredentials(t)
			const key = "jungle-unit-access-key"
			const token = "jungle-unit-session-token"
			switch source {
			case "environment":
				t.Setenv("AWS_ACCESS_KEY_ID", key)
				t.Setenv("AWS_SECRET_ACCESS_KEY", "jungle-unit-secret")
				t.Setenv("AWS_SESSION_TOKEN", token)
			case "shared profile":
				t.Setenv("AWS_PROFILE", "jungle-unit")
				contents := fmt.Sprintf("[jungle-unit]\naws_access_key_id=%s\naws_secret_access_key=jungle-unit-secret\naws_session_token=%s\n", key, token)
				if err := os.WriteFile(file, []byte(contents), 0600); err != nil {
					t.Fatal(err)
				}
			case "container role":
				credentials := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					fmt.Fprintf(w, `{"AccessKeyId":%q,"SecretAccessKey":"jungle-unit-secret","Token":%q,"Expiration":"2099-01-01T00:00:00Z"}`, key, token)
				}))
				t.Cleanup(credentials.Close)
				t.Setenv("AWS_CONTAINER_CREDENTIALS_FULL_URI", credentials.URL)
			}
			var requests atomic.Int32
			broker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				if !strings.Contains(r.Header.Get("Authorization"), "Credential="+key+"/") {
					t.Error("SQS request did not use the selected credential source")
				}
				if r.Header.Get("X-Amz-Security-Token") != token {
					t.Error("SQS request lost the session token")
				}
				w.Header().Set("Content-Type", "application/x-amz-json-1.0")
				fmt.Fprint(w, `{"QueueUrls":[]}`)
			}))
			t.Cleanup(broker.Close)
			client, err := sqsadapter.NewClient(t.Context(), sqsadapter.Config{EndpointURL: broker.URL, Region: "us-east-1"})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := client.ListQueues(t.Context(), &sqs.ListQueuesInput{}); err != nil {
				t.Fatal(err)
			}
			if requests.Load() != 1 {
				t.Fatalf("got %d SQS requests, want 1", requests.Load())
			}
		})
	}
}

func TestSQSClientMissingCredentialsDoesNotSend(t *testing.T) {
	isolatedCredentials(t)
	var requests atomic.Int32
	broker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "application/x-amz-json-1.0")
		fmt.Fprint(w, `{"QueueUrls":[]}`)
	}))
	t.Cleanup(broker.Close)
	client, err := sqsadapter.NewClient(t.Context(), sqsadapter.Config{EndpointURL: broker.URL, Region: "us-east-1"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.ListQueues(t.Context(), &sqs.ListQueuesInput{}); err == nil {
		t.Fatal("missing credentials silently authorized a request")
	}
	if requests.Load() != 0 {
		t.Fatalf("sent %d requests without credentials", requests.Load())
	}
}
