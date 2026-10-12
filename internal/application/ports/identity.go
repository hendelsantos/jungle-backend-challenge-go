package ports

import (
	"context"
	"errors"
)

var ErrUnauthenticated = errors.New("invalid access token")

// Principal contains only claims obtained from a verified access token.
// ProviderID in request JSON never supplies authorization.
type Principal struct {
	Subject    string
	ProviderID string
	Roles      []string
}

func (p Principal) HasRole(role string) bool {
	for _, granted := range p.Roles {
		if granted == role {
			return true
		}
	}
	return false
}

type Authenticator interface {
	Authenticate(ctx context.Context, accessToken string) (Principal, error)
}
