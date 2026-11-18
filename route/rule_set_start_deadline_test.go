package route

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/sagernet/sing-box/adapter"
	C "github.com/sagernet/sing-box/constant"
)

// stalledRuleSet behaves like a remote rule-set whose download server
// accepts the connection and never answers: StartContext returns only when
// its context ends.
type stalledRuleSet struct {
	adapter.RuleSet
}

func (s *stalledRuleSet) StartContext(ctx context.Context, _ *adapter.HTTPStartContext) error {
	<-ctx.Done()
	return ctx.Err()
}

type readyRuleSet struct {
	adapter.RuleSet
}

func (s *readyRuleSet) StartContext(context.Context, *adapter.HTTPStartContext) error {
	return nil
}

func TestStartRuleSetsGivesUpOnAStalledDownload(t *testing.T) {
	done := make(chan error, 1)
	go func() {
		done <- startRuleSets(context.Background(), []adapter.RuleSet{&readyRuleSet{}, &stalledRuleSet{}}, nil, 200*time.Millisecond)
	}()
	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("want a deadline error the client can classify, got %v", err)
		}
		// Clients classify the start error by its text (VilaNet needs
		// "rule-set" together with the deadline), whichever branch of
		// task.Group produced it.
		message := err.Error()
		if !strings.Contains(message, "initialize rule-set") ||
			!strings.Contains(message, "context deadline exceeded") {
			t.Fatalf("start error lost its rule-set provenance: %q", message)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("rule-set start did not end on a stalled download")
	}
}

func TestStartRuleSetsSucceedsWhenEveryRuleSetStarts(t *testing.T) {
	err := startRuleSets(context.Background(), []adapter.RuleSet{&readyRuleSet{}, &readyRuleSet{}}, nil, time.Second)
	if err != nil {
		t.Fatalf("want success, got %v", err)
	}
}

func TestRuleSetStartTimeoutIsBounded(t *testing.T) {
	// The bound must exist and stay well under a user's patience on a
	// "connecting" screen, but above a slow-but-progressing download.
	if c := C.RuleSetStartTimeout; c < 15*time.Second || c > 90*time.Second {
		t.Fatalf("RuleSetStartTimeout %v outside [15s, 90s]", c)
	}
}
