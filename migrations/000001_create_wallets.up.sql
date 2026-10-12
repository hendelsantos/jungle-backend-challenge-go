CREATE TABLE wallets (
    id            uuid PRIMARY KEY,
    player_id     uuid NOT NULL,
    currency      char(3) NOT NULL,
    balance_cents bigint NOT NULL,
    version       bigint NOT NULL,
    created_at    timestamptz NOT NULL,
    updated_at    timestamptz NOT NULL,
    CONSTRAINT wallets_balance_nonnegative CHECK (balance_cents >= 0),
    CONSTRAINT wallets_version_positive CHECK (version >= 1),
    CONSTRAINT wallets_currency_supported CHECK (currency IN ('BRL', 'USD')),
    CONSTRAINT wallets_updated_not_before_created CHECK (updated_at >= created_at)
);

-- One wallet per (player, currency); the application restricts input to BRL.
CREATE UNIQUE INDEX wallets_player_currency_uidx ON wallets (player_id, currency);
