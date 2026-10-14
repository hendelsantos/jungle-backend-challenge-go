// Command verifywallet audits wallets against persistent facts with
// read-only SQL: reconcile the ledger sum against the stored balance, walk
// the ledger chain (no gaps, no negatives), count transactions by kind and
// status, and report the outbox event count. It never writes anything.
//
// Usage:
//
//	verifywallet -url $DATABASE_URL [-final 10.00] [-rows 34] \
//	  [-counts "OPENING=1,BET=33"] [-rejections "BET=17"] [-pending 0] \
//	  [-events-min 3] walletID ...
//
// Exit code is non-zero when any invariant fails; a JSON report always
// prints to stdout.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// report is the audit result for one wallet.
type report struct {
	Wallet        string           `json:"wallet"`
	StoredCents   int64            `json:"storedBalanceCents"`
	ComputedCents int64            `json:"computedBalanceCents"`
	LedgerRows    int              `json:"ledgerRows"`
	Processed     map[string]int64 `json:"processedCounts"`
	Rejected      map[string]int64 `json:"rejectionCounts"`
	PendingRefs   int64            `json:"pendingReferenceCount"`
	OutboxEvents  int64            `json:"outboxEventCount"`
	Checks        map[string]bool  `json:"checks"`
}

// parseMoneyCents converts a decimal string ("10", "10.0", "10.00") into
// cents using integer arithmetic only. Signs are rejected: the oracle takes
// absolute amounts, and overflow is checked before the multiplication.
func parseMoneyCents(text string) (int64, error) {
	if text == "" {
		return 0, errors.New("empty amount")
	}
	intPart, fracPart := text, ""
	if dot := strings.IndexByte(text, '.'); dot >= 0 {
		intPart, fracPart = text[:dot], text[dot+1:]
	}
	if intPart == "" || len(fracPart) > 2 {
		return 0, fmt.Errorf("invalid amount %q", text)
	}
	if strings.ContainsAny(intPart, "+-") || strings.ContainsAny(fracPart, "+-") {
		return 0, fmt.Errorf("invalid amount %q: sign characters are not accepted", text)
	}
	for _, part := range []string{intPart, fracPart} {
		for _, r := range part {
			if r < '0' || r > '9' {
				return 0, fmt.Errorf("invalid amount %q: non-digit %q", text, r)
			}
		}
	}
	for len(fracPart) < 2 {
		fracPart += "0"
	}
	units, err := strconv.ParseInt(intPart, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid units in %q: %w", text, err)
	}
	cents, err := strconv.ParseInt(fracPart, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid fraction in %q: %w", text, err)
	}
	const maxInt64 = int64(9223372036854775807)
	if units > (maxInt64-cents)/100 {
		return 0, fmt.Errorf("amount overflows int64: %q", text)
	}
	return units*100 + cents, nil
}

// parseCounts converts "BET=33,OPENING=1" into the expected map, failing on
// malformed parts so an audit oracle never silently drops an expectation.
func parseCounts(text string) (map[string]int64, error) {
	out := map[string]int64{}
	for _, part := range strings.Split(text, ",") {
		if strings.TrimSpace(part) == "" {
			continue
		}
		name, value, found := strings.Cut(part, "=")
		name = strings.TrimSpace(name)
		if !found || name == "" || strings.ContainsAny(value, "+- ") {
			return nil, fmt.Errorf("invalid counts entry %q", part)
		}
		n, err := strconv.ParseInt(value, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("invalid counts entry %q: %w", part, err)
		}
		if _, duplicate := out[name]; duplicate {
			return nil, fmt.Errorf("duplicate counts entry %q", name)
		}
		out[name] = n
	}
	return out, nil
}

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	fs := flag.NewFlagSet("verifywallet", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	server := fs.String("url", os.Getenv("DATABASE_URL"), "read-only PostgreSQL connection string")
	final := fs.String("final", "", "expected stored balance (decimal string)")
	rows := fs.String("rows", "", "expected ledger row count")
	processedWant := fs.String("counts", "", "expected PROCESSED counts by kind (KIND=N,...)")
	rejectedWant := fs.String("rejections", "", "expected REJECTED counts by kind (KIND=N,...)")
	pending := fs.String("pending", "0", "expected PENDING_REFERENCE count")
	eventsMin := fs.String("events-min", "0", "minimum outbox events for the wallet")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	wallets := fs.Args()
	if *server == "" || len(wallets) == 0 {
		fmt.Fprintln(os.Stderr, "usage: verifywallet -url URL [flags] walletID...")
		return 2
	}
	var wantFinal *int64
	if *final != "" {
		cents, err := parseMoneyCents(*final)
		if err != nil {
			fmt.Fprintf(os.Stderr, "-final: %v\n", err)
			return 2
		}
		wantFinal = &cents
	}
	wantRows, rowsSet, err := optionalInt(*rows)
	if err != nil {
		fmt.Fprintf(os.Stderr, "-rows: %v\n", err)
		return 2
	}
	wantPending, _, err := optionalInt(*pending)
	if err != nil {
		fmt.Fprintf(os.Stderr, "-pending: %v\n", err)
		return 2
	}
	wantEventsMin, _, err := optionalInt(*eventsMin)
	if err != nil {
		fmt.Fprintf(os.Stderr, "-events-min: %v\n", err)
		return 2
	}
	wantProcessed, err := parseCounts(*processedWant)
	if err != nil {
		fmt.Fprintf(os.Stderr, "-counts: %v\n", err)
		return 2
	}
	wantRejected, err := parseCounts(*rejectedWant)
	if err != nil {
		fmt.Fprintf(os.Stderr, "-rejections: %v\n", err)
		return 2
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	conn, err := pgx.Connect(ctx, *server)
	if err != nil {
		fmt.Fprintf(os.Stderr, "connect: %v\n", err)
		return 2
	}
	defer conn.Close(ctx)

	failed := false
	reports := make([]report, 0, len(wallets))
	for _, wallet := range wallets {
		r, issues := audit(ctx, conn, wallet, wantFinal, wantRows, rowsSet,
			wantProcessed, wantRejected, wantPending, wantEventsMin)
		reports = append(reports, r)
		if len(issues) > 0 {
			failed = true
			fmt.Fprintf(os.Stderr, "wallet %s FAILED: %s\n", wallet, strings.Join(issues, "; "))
		} else {
			fmt.Fprintf(os.Stderr, "wallet %s OK\n", wallet)
		}
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(reports)
	if failed {
		return 1
	}
	return 0
}

func optionalInt(text string) (int64, bool, error) {
	if text == "" {
		return 0, false, nil
	}
	n, err := strconv.ParseInt(text, 10, 64)
	if err == nil && n < 0 {
		err = fmt.Errorf("count must be nonnegative")
	}
	return n, true, err
}

func audit(ctx context.Context, conn *pgx.Conn, wallet string, wantFinal *int64,
	wantRows int64, rowsSet bool, wantProcessed, wantRejected map[string]int64,
	wantPending, wantEventsMin int64) (report, []string) {

	r := report{Wallet: wallet, Checks: map[string]bool{}, Processed: map[string]int64{}, Rejected: map[string]int64{}}
	var issues []string
	check := func(name string, ok bool) {
		r.Checks[name] = ok
		if !ok {
			issues = append(issues, name)
		}
	}

	tx, err := conn.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return r, append(issues, fmt.Sprintf("begin audit snapshot: %v", err))
	}
	defer tx.Rollback(context.Background())
	var stored int64
	if err := tx.QueryRow(ctx,
		`SELECT balance_cents FROM wallets WHERE id=$1`, wallet).Scan(&stored); err != nil {
		return r, append(issues, fmt.Sprintf("read wallet: %v", err))
	}
	r.StoredCents = stored

	var computed int64
	if err := tx.QueryRow(ctx, `SELECT
		COALESCE(SUM(CASE WHEN direction='CREDIT' THEN amount_cents END),0)
		- COALESCE(SUM(CASE WHEN direction='DEBIT' THEN amount_cents END),0)
		FROM wallet_ledger_entries WHERE wallet_id=$1`, wallet).Scan(&computed); err != nil {
		return r, append(issues, fmt.Sprintf("sum ledger: %v", err))
	}
	r.ComputedCents = computed

	// Walk the ledger in stable order; each step must account exactly and the
	// chain must end at the stored balance, without a single negative value.
	ledger, err := tx.Query(ctx, `
		SELECT direction, amount_cents, balance_before_cents, balance_after_cents
		FROM wallet_ledger_entries
		WHERE wallet_id=$1
		ORDER BY created_at ASC, id ASC`, wallet)
	if err != nil {
		return r, append(issues, fmt.Sprintf("walk ledger: %v", err))
	}
	var (
		count        int
		previous     int64
		finalAfter   int64
		startBalance int64
		chainOK      = true
		arithmeticOK = true
		nonNegative  = true
	)
	for ledger.Next() {
		var direction string
		var quantity, before, after int64
		if err := ledger.Scan(&direction, &quantity, &before, &after); err != nil {
			ledger.Close()
			return r, append(issues, fmt.Sprintf("scan ledger row: %v", err))
		}
		if count == 0 {
			startBalance = before
		}
		if count > 0 && before != previous {
			chainOK = false
		}
		var expected int64
		if direction == "CREDIT" {
			expected = before + quantity
		} else {
			expected = before - quantity
		}
		if expected != after {
			arithmeticOK = false
		}
		if before < 0 || after < 0 {
			nonNegative = false
		}
		previous = after
		finalAfter = after
		count++
	}
	ledger.Close()
	if err := ledger.Err(); err != nil {
		return r, append(issues, fmt.Sprintf("read ledger: %v", err))
	}
	r.LedgerRows = count

	if count > 0 {
		check("ledger_first_entry_starts_at_zero", startBalance == 0)
		check("ledger_last_entry_reaches_stored_balance", finalAfter == stored)
		check("ledger_chain_continuity", chainOK)
		check("ledger_chain_arithmetic", arithmeticOK)
		check("ledger_no_negative_balances", nonNegative)
		check("ledger_sum_matches_stored_balance", computed == stored)
	} else {
		check("zero_balance_wallet_has_no_ledger", stored == 0)
		check("ledger_sum_matches_stored_balance", computed == stored)
	}
	if rowsSet {
		check("ledger_row_count", count == int(wantRows))
	}
	if wantFinal != nil {
		check("final_balance", stored == *wantFinal)
	}

	// Transaction counts by kind and status, including rejections with their
	// stable failure codes are available on the row itself (failure_code).
	statusCounts := map[string]int64{}
	kinds, err := tx.Query(ctx, `SELECT kind, status, count(*)
		FROM wager_transactions WHERE wallet_id=$1 GROUP BY kind, status`, wallet)
	if err != nil {
		return r, append(issues, fmt.Sprintf("count transactions: %v", err))
	}
	for kinds.Next() {
		var kind, status string
		var n int64
		if err := kinds.Scan(&kind, &status, &n); err != nil {
			kinds.Close()
			return r, append(issues, fmt.Sprintf("scan transaction counts: %v", err))
		}
		statusCounts[status] += n
		if status == "PROCESSED" {
			r.Processed[kind] += n
		}
		if status == "REJECTED" {
			r.Rejected[kind] += n
		}
	}
	kinds.Close()
	if err := kinds.Err(); err != nil {
		return r, append(issues, fmt.Sprintf("read counts: %v", err))
	}
	r.PendingRefs = statusCounts["PENDING_REFERENCE"]

	for kind, want := range wantProcessed {
		check("processed_count_"+strings.ToLower(kind), r.Processed[kind] == want)
	}
	for kind, want := range wantRejected {
		check("rejected_count_"+strings.ToLower(kind), r.Rejected[kind] == want)
	}
	check("pending_reference_count", r.PendingRefs == wantPending)

	var events int64
	if err := tx.QueryRow(ctx,
		`SELECT count(*) FROM outbox_events WHERE payload_json->'data'->>'walletId'=$1`, wallet).Scan(&events); err != nil {
		return r, append(issues, fmt.Sprintf("count outbox events: %v", err))
	}
	r.OutboxEvents = events
	if wantEventsMin > 0 {
		check("outbox_events_minimum", events >= wantEventsMin)
	}
	if err := tx.Commit(ctx); err != nil {
		return r, append(issues, fmt.Sprintf("commit audit snapshot: %v", err))
	}
	return r, issues
}
