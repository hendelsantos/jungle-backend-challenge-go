//go:build integration

package httpapi_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hendelsantos/jungle-backend-challenge-go/internal/bootstrap"
	"github.com/hendelsantos/jungle-backend-challenge-go/internal/testsupport"
	"github.com/jackc/pgx/v5/pgxpool"
)

type httpFixture struct {
	base, issuer string
	pool         *pgxpool.Pool
	client       *http.Client
	tokens       map[string]string
}

func newHTTPFixture(t *testing.T) *httpFixture {
	t.Helper()
	issuer := os.Getenv("TEST_OIDC_ISSUER")
	if issuer == "" {
		t.Skip("TEST_OIDC_ISSUER is not set: real Keycloak is required")
	}
	pool := testsupport.IsolatedPostgresPool(t)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	listener.Close()
	t.Setenv("HTTP_ADDR", address)
	t.Setenv("LOG_LEVEL", "error")
	t.Setenv("DATABASE_URL", pool.Config().ConnString())
	t.Setenv("OIDC_ISSUER", issuer)
	t.Setenv("OIDC_AUDIENCE", "jungle-api")
	app := bootstrap.New()
	if err := app.Err(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	if err := app.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := app.Stop(ctx); err != nil {
			t.Error(err)
		}
	})
	f := &httpFixture{base: "http://" + address, issuer: issuer, pool: pool, client: &http.Client{Timeout: 15 * time.Second}, tokens: map[string]string{}}
	for _, name := range []string{"provider-a-client", "provider-b-client", "wallet-internal-client", "unprivileged-client", "wrong-audience-client"} {
		f.tokens[name] = f.token(t, name)
	}
	return f
}
func (f *httpFixture) token(t *testing.T, name string) string {
	t.Helper()
	values := url.Values{"grant_type": {"client_credentials"}, "client_id": {name}, "client_secret": {"local-" + name + "-secret"}}
	req, err := http.NewRequestWithContext(t.Context(), "POST", f.issuer+"/protocol/openid-connect/token", strings.NewReader(values.Encode()))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	res, err := f.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatalf("real IdP token request returned %d", res.StatusCode)
	}
	var body struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.AccessToken == "" {
		t.Fatal("IdP returned no access token")
	}
	return body.AccessToken
}
func (f *httpFixture) request(t *testing.T, method, path, token, key string, body []byte) (int, map[string]json.RawMessage) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), method, f.base+path, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := f.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	data, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	var parsed map[string]json.RawMessage
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("HTTP %d invalid JSON: %s", res.StatusCode, data)
	}
	return res.StatusCode, parsed
}
func encode(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
func textField(t *testing.T, obj map[string]json.RawMessage, key string) string {
	t.Helper()
	var value string
	if err := json.Unmarshal(obj[key], &value); err != nil {
		t.Fatalf("missing/invalid %s: %v", key, err)
	}
	return value
}
func checkStatus(t *testing.T, got, want int, body map[string]json.RawMessage) {
	t.Helper()
	if got != want {
		t.Fatalf("HTTP %d, want %d: %s", got, want, encode(t, body))
	}
}
func balance(t *testing.T, obj map[string]json.RawMessage) string {
	t.Helper()
	var value struct {
		Amount string `json:"amount"`
	}
	if err := json.Unmarshal(obj["balance"], &value); err != nil {
		t.Fatal(err)
	}
	return value.Amount
}
func (f *httpFixture) wallet(t *testing.T, amount string) (string, string) {
	t.Helper()
	player := uuid.NewString()
	status, body := f.request(t, "POST", "/wallets", f.tokens["wallet-internal-client"], "", encode(t, map[string]any{"playerId": player, "initialBalance": map[string]string{"amount": amount, "currency": "BRL"}}))
	checkStatus(t, status, 201, body)
	return textField(t, body, "id"), player
}
func wager(wallet, player, kind, amount string) map[string]any {
	return map[string]any{
		"providerId": "provider-a", "externalTransactionId": uuid.NewString(), "walletId": wallet, "playerId": player,
		"roundId": "round-1", "gameId": "game-1", "kind": kind, "money": map[string]string{"amount": amount, "currency": "BRL"},
	}
}
func (f *httpFixture) effects(t *testing.T, wallet string) [5]int64 {
	t.Helper()
	var result [5]int64
	err := f.pool.QueryRow(t.Context(), `SELECT balance_cents,version,
 (SELECT count(*) FROM wager_transactions WHERE wallet_id=$1),
 (SELECT count(*) FROM wallet_ledger_entries WHERE wallet_id=$1),
 (SELECT count(*) FROM outbox_events WHERE payload_json->'data'->>'walletId'=$2)
 FROM wallets WHERE id=$1`, wallet, wallet).Scan(&result[0], &result[1], &result[2], &result[3], &result[4])
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestAuthenticatedFinancialHTTPIntegration(t *testing.T) {
	f := newHTTPFixture(t)
	a, b, internal := f.tokens["provider-a-client"], f.tokens["provider-b-client"], f.tokens["wallet-internal-client"]
	t.Run("public readiness probes the real database", func(t *testing.T) {
		status, body := f.request(t, "GET", "/health/ready", "", "", nil)
		checkStatus(t, status, 200, body)
		var checks struct {
			Status string            `json:"status"`
			Checks map[string]string `json:"checks"`
		}
		if err := json.Unmarshal(encode(t, body), &checks); err != nil {
			t.Fatal(err)
		}
		if checks.Status != "ready" || checks.Checks["postgres"] != "ok" {
			t.Fatalf("readiness = %s", encode(t, body))
		}
	})
	t.Run("authentication and permissions reject before financial effects", func(t *testing.T) {
		wallet, player := f.wallet(t, "100.00")
		input := encode(t, wager(wallet, player, "BET", "25.00"))
		before := f.effects(t, wallet)
		parts := strings.Split(a, ".")
		sig, err := base64.RawURLEncoding.DecodeString(parts[2])
		if err != nil {
			t.Fatal(err)
		}
		sig[0] ^= 1
		tampered := parts[0] + "." + parts[1] + "." + base64.RawURLEncoding.EncodeToString(sig)
		for _, tc := range []struct {
			name, token string
			want        int
		}{
			{"missing", "", 401}, {"malformed", "garbage", 401}, {"tampered", tampered, 401},
			{"wrong audience", f.tokens["wrong-audience-client"], 401}, {"no role", f.tokens["unprivileged-client"], 403}, {"internal cannot wager", internal, 403},
		} {
			t.Run(tc.name, func(t *testing.T) {
				status, body := f.request(t, "POST", "/wagering/transactions", tc.token, "auth-test", input)
				checkStatus(t, status, tc.want, body)
			})
		}
		for _, path := range []string{"/wallets/" + wallet, "/wallets/" + wallet + "/ledger"} {
			status, body := f.request(t, "GET", path, a, "", nil)
			checkStatus(t, status, 403, body)
		}
		status, body := f.request(t, "POST", "/wallets/"+wallet+"/reconciliation", a, "", nil)
		checkStatus(t, status, 403, body)
		status, body = f.request(t, "POST", "/wallets", a, "", []byte(`{}`))
		checkStatus(t, status, 403, body)
		if after := f.effects(t, wallet); after != before {
			t.Fatalf("unauthorized effects: %v -> %v", before, after)
		}
	})
	t.Run("five operations replay queries ledger and reconciliation", func(t *testing.T) {
		wallet, player := f.wallet(t, "100.00")
		bet := wager(wallet, player, "BET", "25.00")
		key := uuid.NewString()
		status, first := f.request(t, "POST", "/wagering/transactions", a, key, encode(t, bet))
		checkStatus(t, status, 200, first)
		if balance(t, first) != "75.00" {
			t.Fatal("wrong bet balance")
		}
		txID := textField(t, first, "transactionId")
		// Another operation changes current balance; historical replay stays at 75.
		win := wager(wallet, player, "WIN", "40.00")
		win["referenceExternalTransactionId"] = bet["externalTransactionId"]
		status, body := f.request(t, "POST", "/wagering/transactions", a, uuid.NewString(), encode(t, win))
		checkStatus(t, status, 200, body)
		if balance(t, body) != "115.00" {
			t.Fatal("wrong win balance")
		}
		status, replay := f.request(t, "POST", "/wagering/transactions", a, key, encode(t, bet))
		checkStatus(t, status, 200, replay)
		if balance(t, replay) != "75.00" || string(replay["idempotentReplay"]) != "true" || textField(t, replay, "transactionId") != txID {
			t.Fatal("historical replay changed")
		}
		for _, path := range []string{"/wagering/transactions/" + txID, "/providers/provider-a/wagering/transactions/" + bet["externalTransactionId"].(string)} {
			status, body = f.request(t, "GET", path, a, "", nil)
			checkStatus(t, status, 200, body)
			if textField(t, body, "transactionId") != txID {
				t.Fatal("wrong query result")
			}
		}
		before := f.effects(t, wallet)
		status, body = f.request(t, "POST", "/wagering/transactions", a, uuid.NewString(), encode(t, wager(wallet, player, "LOSS", "0.00")))
		checkStatus(t, status, 200, body)
		after := f.effects(t, wallet)
		if after[0] != before[0] || after[1] != before[1] || after[3] != before[3] || after[4] != before[4]+1 {
			t.Fatalf("LOSS effects: %v -> %v", before, after)
		}
		refund := wager(wallet, player, "REFUND", "25.00")
		refund["referenceExternalTransactionId"] = bet["externalTransactionId"]
		status, body = f.request(t, "POST", "/wagering/transactions", a, uuid.NewString(), encode(t, refund))
		checkStatus(t, status, 200, body)
		rollback := wager(wallet, player, "ROLLBACK", "25.00")
		rollback["referenceExternalTransactionId"] = refund["externalTransactionId"]
		status, body = f.request(t, "POST", "/wagering/transactions", a, uuid.NewString(), encode(t, rollback))
		checkStatus(t, status, 200, body)
		if balance(t, body) != "115.00" {
			t.Fatal("wrong rollback balance")
		}
		status, body = f.request(t, "GET", "/wallets/"+wallet, internal, "", nil)
		checkStatus(t, status, 200, body)
		if balance(t, body) != "115.00" {
			t.Fatal("wrong wallet balance")
		}
		status, body = f.request(t, "GET", "/wallets/"+wallet+"/ledger?limit=2", internal, "", nil)
		checkStatus(t, status, 200, body)
		cursor := textField(t, body, "nextCursor")
		status, body = f.request(t, "GET", "/wallets/"+wallet+"/ledger?limit=2&cursor="+url.QueryEscape(cursor), internal, "", nil)
		checkStatus(t, status, 200, body)
		status, body = f.request(t, "POST", "/wallets/"+wallet+"/reconciliation", internal, "", nil)
		checkStatus(t, status, 200, body)
		if string(body["consistent"]) != "true" {
			t.Fatalf("inconsistent: %s", encode(t, body))
		}
	})
	t.Run("provider isolation applies to reads replays and conflicts", func(t *testing.T) {
		wallet, player := f.wallet(t, "100.00")
		bet := wager(wallet, player, "BET", "25.00")
		key := uuid.NewString()
		status, first := f.request(t, "POST", "/wagering/transactions", a, key, encode(t, bet))
		checkStatus(t, status, 200, first)
		txID := textField(t, first, "transactionId")
		before := f.effects(t, wallet)
		for _, keyToUse := range []string{key, uuid.NewString()} {
			status, body := f.request(t, "POST", "/wagering/transactions", b, keyToUse, encode(t, bet))
			checkStatus(t, status, 403, body)
		}
		status, body := f.request(t, "GET", "/wagering/transactions/"+txID, b, "", nil)
		checkStatus(t, status, 404, body)
		status, body = f.request(t, "GET", "/providers/provider-a/wagering/transactions/"+bet["externalTransactionId"].(string), b, "", nil)
		checkStatus(t, status, 403, body)
		status, body = f.request(t, "GET", "/providers/provider-b/wagering/transactions/"+bet["externalTransactionId"].(string), b, "", nil)
		checkStatus(t, status, 404, body)
		if after := f.effects(t, wallet); after != before {
			t.Fatal("cross-provider access changed state")
		}
		// Legitimate provider B may reuse the same identity in its own namespace.
		bet["providerId"] = "provider-b"
		status, body = f.request(t, "POST", "/wagering/transactions", b, key, encode(t, bet))
		checkStatus(t, status, 200, body)
		if textField(t, body, "transactionId") == txID || string(body["idempotentReplay"]) != "false" {
			t.Fatal("provider namespaces collided")
		}
	})
	t.Run("conflicts rejected and pending contracts", func(t *testing.T) {
		wallet, player := f.wallet(t, "100.00")
		bet := wager(wallet, player, "BET", "80.00")
		key := uuid.NewString()
		status, body := f.request(t, "POST", "/wagering/transactions", a, key, encode(t, bet))
		checkStatus(t, status, 200, body)
		status, body = f.request(t, "POST", "/wagering/transactions", a, uuid.NewString(), encode(t, bet))
		checkStatus(t, status, 409, body)
		bet["money"] = map[string]string{"amount": "81.00", "currency": "BRL"}
		status, body = f.request(t, "POST", "/wagering/transactions", a, key, encode(t, bet))
		checkStatus(t, status, 409, body)
		rejected := wager(wallet, player, "BET", "80.00")
		rejectedKey := uuid.NewString()
		status, body = f.request(t, "POST", "/wagering/transactions", a, rejectedKey, encode(t, rejected))
		checkStatus(t, status, 422, body)
		if textField(t, body, "failureCode") != "INSUFFICIENT_FUNDS" {
			t.Fatal("missing stable rejection code")
		}
		status, body = f.request(t, "POST", "/wagering/transactions", a, rejectedKey, encode(t, rejected))
		checkStatus(t, status, 422, body)
		if string(body["idempotentReplay"]) != "true" {
			t.Fatal("rejection not replayed")
		}
		pending := wager(wallet, player, "REFUND", "25.00")
		pending["referenceExternalTransactionId"] = uuid.NewString()
		pendingKey := uuid.NewString()
		status, body = f.request(t, "POST", "/wagering/transactions", a, pendingKey, encode(t, pending))
		checkStatus(t, status, 202, body)
		pendingID := textField(t, body, "transactionId")
		if _, exists := body["balance"]; exists {
			t.Fatal("pending response invented a balance")
		}
		status, body = f.request(t, "GET", "/wagering/transactions/"+pendingID, a, "", nil)
		checkStatus(t, status, 200, body)
		if textField(t, body, "status") != "PENDING_REFERENCE" {
			t.Fatal("pending lookup lost status")
		}
	})
	t.Run("invalid contracts have no effects", func(t *testing.T) {
		wallet, player := f.wallet(t, "100.00")
		before := f.effects(t, wallet)
		input := wager(wallet, player, "BET", "25.00")
		valid := encode(t, input)
		invalid := [][]byte{
			[]byte(`null`), append(append([]byte{}, valid...), []byte(` {}`)...),
			[]byte(strings.Replace(string(valid), `"providerId":"provider-a"`, `"providerId":"provider-a","providerId":"provider-b"`, 1)),
			[]byte(strings.Replace(string(valid), `"amount":"25.00"`, `"amount":25.00`, 1)),
			[]byte(strings.Replace(string(valid), `"amount":"25.00"`, `"amount":"25.001"`, 1)),
			[]byte(strings.Replace(string(valid), `"kind":"BET"`, `"kind":"OPENING"`, 1)),
			[]byte(strings.Replace(string(valid), `"kind":"BET"`, `"kind":null`, 1)),
			[]byte(strings.Replace(string(valid), `"kind":"BET"`, `"kind":"BET","extra":"field"`, 1)),
		}
		for _, body := range invalid {
			status, response := f.request(t, "POST", "/wagering/transactions", a, uuid.NewString(), body)
			checkStatus(t, status, 400, response)
		}
		status, response := f.request(t, "POST", "/wagering/transactions", a, "", valid)
		checkStatus(t, status, 400, response)
		for _, suffix := range []string{"?limit=0", "?limit=101", "?limit=1&limit=2", "?cursor=invalid", "?bad=1"} {
			status, response = f.request(t, "GET", "/wallets/"+wallet+"/ledger"+suffix, internal, "", nil)
			checkStatus(t, status, 400, response)
		}
		if after := f.effects(t, wallet); after != before {
			t.Fatalf("invalid input changed state: %v -> %v", before, after)
		}
	})
	t.Run("expired token from real IdP", func(t *testing.T) {
		token := f.token(t, "expired-client")
		payload, err := base64.RawURLEncoding.DecodeString(strings.Split(token, ".")[1])
		if err != nil {
			t.Fatal(err)
		}
		var claims struct {
			Expiry int64 `json:"exp"`
		}
		if err := json.Unmarshal(payload, &claims); err != nil {
			t.Fatal(err)
		}
		delay := time.Until(time.Unix(claims.Expiry, 0)) + 100*time.Millisecond
		if delay > 10*time.Second {
			t.Fatal("expired-client must use a short test lifespan")
		}
		if delay > 0 {
			timer := time.NewTimer(delay)
			defer timer.Stop()
			select {
			case <-timer.C:
			case <-t.Context().Done():
				t.Fatal(t.Context().Err())
			}
		}
		status, body := f.request(t, "GET", "/wagering/transactions/"+uuid.NewString(), token, "", nil)
		checkStatus(t, status, 401, body)
	})
	t.Run("two HTTP bets of 80 over 100", func(t *testing.T) {
		wallet, player := f.wallet(t, "100.00")
		requests := [][]byte{encode(t, wager(wallet, player, "BET", "80.00")), encode(t, wager(wallet, player, "BET", "80.00"))}
		codes := make([]int, 2)
		bodies := make([]map[string]json.RawMessage, 2)
		var wg sync.WaitGroup
		start := make(chan struct{})
		for i := range 2 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				codes[i], bodies[i] = f.request(t, "POST", "/wagering/transactions", a, uuid.NewString(), requests[i])
			}()
		}
		close(start)
		wg.Wait()
		if !((codes[0] == 200 && codes[1] == 422) || (codes[0] == 422 && codes[1] == 200)) {
			t.Fatalf("statuses=%v", codes)
		}
		if got := f.effects(t, wallet); got != [5]int64{2000, 2, 3, 2, 5} {
			t.Fatalf("concurrent effects=%v", got)
		}
	})
}
