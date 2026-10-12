package command

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/hendelsantos/jungle-backend-challenge-go/internal/application/ports"
	"github.com/hendelsantos/jungle-backend-challenge-go/internal/domain/wagering"
	"github.com/hendelsantos/jungle-backend-challenge-go/internal/domain/wallet"
)

func consumeInput(input ProcessWagerInput) ConsumeWagerInput {
	return ConsumeWagerInput{ConsumerName: "wager-consumer", MessageID: "msg-1", PayloadHash: otherHash, Wager: input}
}

type fakeInbox struct {
	completions []ports.InboxCompletion
	replayed    bool
	conflict    bool
	err         error
}

func (f *fakeInbox) TryCompleteInbox(_ context.Context, completion ports.InboxCompletion) (bool, error) {
	if f.err != nil {
		return false, f.err
	}
	if f.conflict {
		return false, ports.ErrInboxConflict
	}
	if f.replayed {
		return true, nil
	}
	f.completions = append(f.completions, completion)
	return false, nil
}

func newConsumeDeps(locked wallet.State, inbox *fakeInbox) ConsumeWagerDeps {
	process := newProcessFakes(locked)
	return ConsumeWagerDeps{
		UnitOfWork: process.uow,
		Process:    process.deps(),
		Inbox:      inbox,
		Now:        func() time.Time { return processTime },
		Backoff:    func(int) time.Duration { return 0 },
	}
}

func TestConsumeWagerMessageCompletesInboxWithTransaction(t *testing.T) {
	locked := lockedWalletState(10000)
	inbox := &fakeInbox{}
	deps := newConsumeDeps(locked, inbox)
	result, err := deps.ConsumeWagerMessage(context.Background(), consumeInput(betInput(locked, 2500)))
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != wagering.Processed || result.Replay || result.InboxReplay {
		t.Fatalf("result = %+v", result)
	}
	if len(inbox.completions) != 1 {
		t.Fatalf("inbox completions = %d", len(inbox.completions))
	}
	completion := inbox.completions[0]
	if completion.ConsumerName != "wager-consumer" || completion.MessageID != "msg-1" ||
		completion.TransactionID != result.TransactionID || !completion.CompletedAt.Equal(processTime) {
		t.Fatalf("completion = %+v", completion)
	}
}

func TestConsumeWagerMessageReportsInboxReplay(t *testing.T) {
	locked := lockedWalletState(10000)
	inbox := &fakeInbox{replayed: true}
	deps := newConsumeDeps(locked, inbox)
	result, err := deps.ConsumeWagerMessage(context.Background(), consumeInput(betInput(locked, 2500)))
	if err != nil {
		t.Fatal(err)
	}
	if !result.InboxReplay || len(inbox.completions) != 0 {
		t.Fatalf("result = %+v, completions = %d", result, len(inbox.completions))
	}
}

func TestConsumeWagerMessageInboxConflictIsPermanent(t *testing.T) {
	locked := lockedWalletState(10000)
	deps := newConsumeDeps(locked, &fakeInbox{conflict: true})
	if _, err := deps.ConsumeWagerMessage(context.Background(), consumeInput(betInput(locked, 2500))); !errors.Is(err, ErrPermanentMessage) {
		t.Fatalf("err = %v, want ErrPermanentMessage", err)
	}
}

func TestConsumeWagerMessageContractFailureIsPermanent(t *testing.T) {
	locked := lockedWalletState(10000)
	input := betInput(locked, 2500)
	input.Kind = wagering.Opening
	deps := newConsumeDeps(locked, &fakeInbox{})
	if _, err := deps.ConsumeWagerMessage(context.Background(), consumeInput(input)); !errors.Is(err, ErrPermanentMessage) {
		t.Fatalf("err = %v, want ErrPermanentMessage", err)
	}
}

func TestConsumeWagerMessageRetriesDeadlockedTransaction(t *testing.T) {
	locked := lockedWalletState(10000)
	inbox := &fakeInbox{}
	deps := newConsumeDeps(locked, inbox)
	deps.UnitOfWork = &recordingUoW{failures: []error{ports.ErrRetryableTransaction}}
	if _, err := deps.ConsumeWagerMessage(context.Background(), consumeInput(betInput(locked, 2500))); err != nil {
		t.Fatal(err)
	}
	if len(inbox.completions) != 1 {
		t.Fatalf("inbox completions = %d", len(inbox.completions))
	}
}

func TestConsumeWagerMessageInfrastructureErrorIsNotRetried(t *testing.T) {
	locked := lockedWalletState(10000)
	deps := newConsumeDeps(locked, &fakeInbox{err: errors.New("connection reset")})
	if _, err := deps.ConsumeWagerMessage(context.Background(), consumeInput(betInput(locked, 2500))); err == nil {
		t.Fatal("expected infrastructure error")
	}
}

func TestConsumeWagerMessageRequiresDeps(t *testing.T) {
	deps := ConsumeWagerDeps{Now: func() time.Time { return processTime }}
	if err := deps.validate(); !errors.Is(err, ErrIncompleteDeps) {
		t.Fatalf("err = %v", err)
	}
}
