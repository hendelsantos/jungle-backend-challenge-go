DROP TRIGGER wallet_ledger_entries_no_truncate ON wallet_ledger_entries;
DROP TRIGGER wallet_ledger_entries_no_update ON wallet_ledger_entries;
DROP FUNCTION forbid_ledger_mutation();
DROP TABLE wallet_ledger_entries;
