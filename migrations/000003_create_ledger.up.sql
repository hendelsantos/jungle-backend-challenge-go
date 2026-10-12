CREATE TABLE wallet_ledger_entries (
    id                   uuid PRIMARY KEY,
    wallet_id            uuid NOT NULL REFERENCES wallets (id),
    transaction_id       uuid NOT NULL REFERENCES wager_transactions (id),
    direction            text NOT NULL,
    amount_cents         bigint NOT NULL,
    balance_before_cents bigint NOT NULL,
    balance_after_cents  bigint NOT NULL,
    created_at           timestamptz NOT NULL,
    CONSTRAINT wallet_ledger_entries_direction_valid CHECK (
        direction IN ('DEBIT', 'CREDIT')
    ),
    CONSTRAINT wallet_ledger_entries_amount_positive CHECK (
        amount_cents > 0
    ),
    CONSTRAINT wallet_ledger_entries_balances_nonnegative CHECK (
        balance_before_cents >= 0 AND balance_after_cents >= 0
    ),
    -- Addition and subtraction beyond BIGINT raise an SQL numeric error on insert.
    CONSTRAINT wallet_ledger_entries_arithmetic CHECK (
        (direction = 'CREDIT' AND balance_after_cents = balance_before_cents + amount_cents)
        OR (direction = 'DEBIT' AND balance_after_cents = balance_before_cents - amount_cents)
    )
);

-- Exactly one entry per effective movement of a transaction on a wallet.
CREATE UNIQUE INDEX wallet_ledger_entries_wallet_transaction_uidx
    ON wallet_ledger_entries (wallet_id, transaction_id);

-- Append-only enforcement: financial corrections require new entries.
CREATE FUNCTION forbid_ledger_mutation() RETURNS trigger AS $$
BEGIN
    RAISE EXCEPTION 'wallet_ledger_entries is append-only';
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER wallet_ledger_entries_no_update
    BEFORE UPDATE OR DELETE ON wallet_ledger_entries
    FOR EACH ROW
    EXECUTE FUNCTION forbid_ledger_mutation();

CREATE TRIGGER wallet_ledger_entries_no_truncate
    BEFORE TRUNCATE ON wallet_ledger_entries
    FOR EACH STATEMENT
    EXECUTE FUNCTION forbid_ledger_mutation();
