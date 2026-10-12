// Package oidcauth verifies Keycloak access tokens. It never issues tokens.
package oidcauth

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/hendelsantos/jungle-backend-challenge-go/internal/application/ports"
)

type Config struct{ Issuer, Audience string }

func (c Config) Validate() error {
	u, err := url.Parse(c.Issuer)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil || u.RawQuery != "" || u.Fragment != "" || strings.TrimSpace(c.Audience) == "" {
		return fmt.Errorf("OIDC requires an issuer URL and non-empty audience")
	}
	return nil
}

type Verifier struct {
	config   Config
	verifier atomic.Pointer[oidc.IDTokenVerifier]
	client   *http.Client
}

var _ ports.Authenticator = (*Verifier)(nil)

func NewVerifier(config Config) (*Verifier, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	// A separate transport is closed after HTTP drains during Fx shutdown.
	transport := http.DefaultTransport.(*http.Transport).Clone()
	return &Verifier{config: config, client: &http.Client{Timeout: 5 * time.Second, Transport: transport}}, nil
}

// Start performs discovery before serving traffic. RemoteKeySet caches keys and
// refreshes them when a signature uses a new key, supporting IdP key rotation.
func (v *Verifier) Start(ctx context.Context) error {
	ctx = oidc.ClientContext(ctx, v.client)
	provider, err := oidc.NewProvider(ctx, v.config.Issuer)
	if err != nil {
		return fmt.Errorf("OIDC discovery: %w", err)
	}
	// VerifierContext needs a lifetime independent of the startup deadline.
	lifetime := oidc.ClientContext(context.Background(), v.client)
	v.verifier.Store(provider.VerifierContext(lifetime, &oidc.Config{
		ClientID: v.config.Audience, SupportedSigningAlgs: []string{oidc.RS256},
	}))
	return nil
}

func (v *Verifier) Close() { v.client.CloseIdleConnections() }

func (v *Verifier) Authenticate(ctx context.Context, raw string) (ports.Principal, error) {
	verifier := v.verifier.Load()
	if verifier == nil || raw == "" {
		return ports.Principal{}, ports.ErrUnauthenticated
	}
	token, err := verifier.Verify(ctx, raw)
	if err != nil {
		return ports.Principal{}, ports.ErrUnauthenticated
	}
	var claims struct {
		Type            string `json:"typ"`
		AuthorizedParty string `json:"azp"`
		ProviderID      string `json:"provider_id"`
		NotBefore       int64  `json:"nbf"`
		RealmAccess     struct {
			Roles []string `json:"roles"`
		} `json:"realm_access"`
	}
	if err := token.Claims(&claims); err != nil || token.Subject == "" || claims.Type != "Bearer" || claims.AuthorizedParty == "" || claims.NotBefore > time.Now().Unix() {
		return ports.Principal{}, ports.ErrUnauthenticated
	}
	return ports.Principal{Subject: token.Subject, ProviderID: claims.ProviderID, Roles: claims.RealmAccess.Roles}, nil
}
