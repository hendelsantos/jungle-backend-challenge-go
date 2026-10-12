package query

import (
	"context"
	"errors"
	"github.com/google/uuid"
	"github.com/hendelsantos/jungle-backend-challenge-go/internal/application/ports"
	"github.com/hendelsantos/jungle-backend-challenge-go/internal/domain/wagering"
	"strings"
)

var ErrInvalidWagerQuery = errors.New("provider and transaction identity are required")

type GetWagerDeps struct{ Wagers ports.WagerQueryReader }

func (d GetWagerDeps) ByID(ctx context.Context, provider string, id uuid.UUID) (wagering.State, error) {
	if d.Wagers == nil {
		return wagering.State{}, ErrIncompleteDeps
	}
	if strings.TrimSpace(provider) == "" || id == uuid.Nil {
		return wagering.State{}, ErrInvalidWagerQuery
	}
	return d.Wagers.FindByID(ctx, provider, id)
}
func (d GetWagerDeps) ByExternalID(ctx context.Context, provider, external string) (wagering.State, error) {
	if d.Wagers == nil {
		return wagering.State{}, ErrIncompleteDeps
	}
	if strings.TrimSpace(provider) == "" || strings.TrimSpace(external) == "" {
		return wagering.State{}, ErrInvalidWagerQuery
	}
	return d.Wagers.FindByExternalID(ctx, provider, external)
}
