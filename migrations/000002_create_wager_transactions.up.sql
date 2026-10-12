CREATE TABLE wager_transactions (
    id                                uuid PRIMARY KEY,
    wallet_id                         uuid NOT NULL REFERENCES wallets (id),
    player_id                         uuid NOT NULL,
    kind                              text NOT NULL,
    status                            text NOT NULL,
    amount_cents                      bigint NOT NULL,
    currency                          char(3) NOT NULL,
    provider_id                       text,
    external_transaction_id           text,
    idempotency_key                   text,
    payload_hash                      text,
    round_id                          text,
    game_id                           text,
    reference_external_transaction_id text,
    reference_transaction_id          uuid REFERENCES wager_transactions (id),
    failure_code                      text,
    result_balance_cents              bigint,
    result_wallet_version             bigint,
    attempt_count                     integer NOT NULL DEFAULT 0,
    next_attempt_at                   timestamptz,
    reference_deadline_at             timestamptz,
    created_at                        timestamptz NOT NULL,
    updated_at                        timestamptz NOT NULL,
    CONSTRAINT wager_transactions_kind_valid CHECK (
        kind IN ('OPENING', 'BET', 'WIN', 'LOSS', 'REFUND', 'ROLLBACK')
    ),
    CONSTRAINT wager_transactions_status_valid CHECK (
        status IN ('PENDING', 'PENDING_REFERENCE', 'PROCESSED', 'REJECTED', 'FAILED')
    ),
    CONSTRAINT wager_transactions_currency_supported CHECK (
        currency IN ('BRL', 'USD')
    ),
    CONSTRAINT wager_transactions_amount_by_kind CHECK (
        (kind = 'OPENING' AND amount_cents >= 0)
        OR (kind IN ('BET', 'WIN', 'REFUND', 'ROLLBACK') AND amount_cents > 0)
        OR (kind = 'LOSS' AND amount_cents = 0)
    ),
    CONSTRAINT wager_transactions_external_metadata CHECK (
        (
            kind = 'OPENING'
            AND provider_id IS NULL
            AND external_transaction_id IS NULL
            AND idempotency_key IS NULL
            AND payload_hash IS NULL
            AND round_id IS NULL
            AND game_id IS NULL
            AND reference_external_transaction_id IS NULL
        )
        OR (
            kind <> 'OPENING'
            AND provider_id IS NOT NULL
            AND provider_id <> ''
            AND external_transaction_id IS NOT NULL
            AND external_transaction_id <> ''
            AND idempotency_key IS NOT NULL
            AND idempotency_key <> ''
            AND payload_hash IS NOT NULL
            AND payload_hash <> ''
            AND round_id IS NOT NULL
            AND round_id <> ''
            AND game_id IS NOT NULL
            AND game_id <> ''
        )
    ),
    CONSTRAINT wager_transactions_payload_hash_format CHECK (
        payload_hash IS NULL OR payload_hash ~ '^[0-9a-f]{64}$'
    ),
    CONSTRAINT wager_transactions_reference_by_kind CHECK (
        (kind IN ('REFUND', 'ROLLBACK')
            AND reference_external_transaction_id IS NOT NULL
            AND reference_external_transaction_id <> '')
        OR kind = 'WIN'
        OR (kind IN ('BET', 'LOSS', 'OPENING')
            AND reference_external_transaction_id IS NULL)
    ),
    CONSTRAINT wager_transactions_reference_not_self CHECK (
        reference_transaction_id IS NULL OR reference_transaction_id <> id
    ),
    CONSTRAINT wager_transactions_failure_code_presence CHECK (
        (status IN ('REJECTED', 'FAILED')) = (failure_code IS NOT NULL)
    ),
    CONSTRAINT wager_transactions_failure_code_valid CHECK (
        failure_code IS NULL
        OR failure_code IN (
            'INSUFFICIENT_FUNDS',
            'REVERSAL_INSUFFICIENT_FUNDS',
            'REFERENCE_NOT_FOUND',
            'REFERENCE_NOT_PROCESSED',
            'REFERENCE_SCOPE_MISMATCH',
            'REFERENCE_AMOUNT_MISMATCH',
            'INVALID_REFERENCE_KIND',
            'ALREADY_REVERSED',
            'WALLET_CURRENCY_MISMATCH',
            'BALANCE_OVERFLOW',
            'PERMANENT_INFRASTRUCTURE_FAILURE'
        )
    ),
    CONSTRAINT wager_transactions_result_pair CHECK (
        (result_balance_cents IS NULL AND result_wallet_version IS NULL)
        OR (result_balance_cents IS NOT NULL AND result_wallet_version IS NOT NULL)
    ),
    CONSTRAINT wager_transactions_result_nonnegative CHECK (
        result_balance_cents IS NULL
        OR (result_balance_cents >= 0 AND result_wallet_version >= 1)
    ),
    CONSTRAINT wager_transactions_processed_has_result CHECK (
        status <> 'PROCESSED'
        OR (result_balance_cents IS NOT NULL AND result_wallet_version IS NOT NULL)
    ),
    CONSTRAINT wager_transactions_opening_result CHECK (
        kind <> 'OPENING'
        OR status <> 'PROCESSED'
        OR (result_balance_cents = amount_cents AND result_wallet_version = 1)
    ),
    CONSTRAINT wager_transactions_pending_states_pure CHECK (
        status NOT IN ('PENDING', 'PENDING_REFERENCE')
        OR (
            result_balance_cents IS NULL
            AND failure_code IS NULL
            AND reference_transaction_id IS NULL
        )
    ),
    CONSTRAINT wager_transactions_failed_has_no_result CHECK (
        status <> 'FAILED'
        OR (result_balance_cents IS NULL AND result_wallet_version IS NULL)
    ),
    CONSTRAINT wager_transactions_updated_not_before_created CHECK (
        updated_at >= created_at
    )
);

-- Durable idempotency: one business operation per (provider, key) and per
-- (provider, external transaction ID), for external origin only.
CREATE UNIQUE INDEX wager_transactions_provider_idempotency_uidx
    ON wager_transactions (provider_id, idempotency_key)
    WHERE kind <> 'OPENING';

CREATE UNIQUE INDEX wager_transactions_provider_external_uidx
    ON wager_transactions (provider_id, external_transaction_id)
    WHERE kind <> 'OPENING';

-- One internal opening per wallet, with a stable identity reused on retries.
CREATE UNIQUE INDEX wager_transactions_opening_wallet_uidx
    ON wager_transactions (wallet_id)
    WHERE kind = 'OPENING';

-- ADR 0001: exactly one successful direct compensation per referenced
-- transaction, regardless of whether it is a REFUND or a ROLLBACK.
CREATE UNIQUE INDEX wager_transactions_direct_compensation_uidx
    ON wager_transactions (reference_transaction_id)
    WHERE kind IN ('REFUND', 'ROLLBACK') AND status = 'PROCESSED';

CREATE INDEX wager_transactions_wallet_idx ON wager_transactions (wallet_id);

CREATE INDEX wager_transactions_pending_reference_idx
    ON wager_transactions (next_attempt_at)
    WHERE status = 'PENDING_REFERENCE';
