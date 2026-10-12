CREATE TABLE inbox_messages (
    consumer_name  text NOT NULL,
    message_id     text NOT NULL,
    payload_hash   text NOT NULL,
    received_at    timestamptz NOT NULL,
    completed_at   timestamptz,
    transaction_id uuid REFERENCES wager_transactions (id),
    PRIMARY KEY (consumer_name, message_id),
    CONSTRAINT inbox_messages_completed_after_received CHECK (
        completed_at IS NULL OR completed_at >= received_at
    )
);

CREATE TABLE outbox_events (
    event_id        uuid PRIMARY KEY,
    aggregate_id    uuid NOT NULL,
    event_type      text NOT NULL,
    payload_json    jsonb NOT NULL,
    occurred_at     timestamptz NOT NULL,
    attempts        integer NOT NULL DEFAULT 0,
    next_attempt_at timestamptz NOT NULL,
    published_at    timestamptz,
    leased_by       text,
    leased_until    timestamptz,
    last_error      text,
    created_at      timestamptz NOT NULL,
    CONSTRAINT outbox_events_type_valid CHECK (
        event_type IN (
            'WagerTransactionProcessed',
            'WagerTransactionRejected',
            'WalletBalanceChanged',
            'WagerTransactionPendingReference'
        )
    ),
    CONSTRAINT outbox_events_attempts_nonnegative CHECK (
        attempts >= 0
    ),
    CONSTRAINT outbox_events_lease_pair CHECK (
        (leased_by IS NULL AND leased_until IS NULL)
        OR (leased_by IS NOT NULL AND leased_by <> '' AND leased_until IS NOT NULL)
    ),
    CONSTRAINT outbox_events_created_not_before_occurred CHECK (
        created_at >= occurred_at
    )
);

-- Pending and retryable events, in next-attempt order for the publisher.
CREATE INDEX outbox_events_unpublished_idx
    ON outbox_events (next_attempt_at)
    WHERE published_at IS NULL;

CREATE INDEX outbox_events_aggregate_idx ON outbox_events (aggregate_id, occurred_at);
