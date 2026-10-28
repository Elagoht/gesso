package events

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
)

type deleted struct{ Slug string }

var noteDeleted = Topic[deleted]{Name: "notes.deleted"}

func TestPublishReachesSubscribersInOrder(t *testing.T) {
	b := NewBus()
	ctx := context.Background()
	if err := noteDeleted.Publish(ctx, b, deleted{Slug: "x"}); err != nil {
		t.Fatalf("no subscriber is no error: %v", err)
	}
	var got []string
	noteDeleted.Subscribe(b, func(_ context.Context, e deleted) error { got = append(got, "search "+e.Slug); return nil })
	noteDeleted.Subscribe(b, func(_ context.Context, e deleted) error { got = append(got, "stats "+e.Slug); return nil })
	if err := noteDeleted.Publish(ctx, b, deleted{Slug: "jam"}); err != nil {
		t.Fatal(err)
	}
	if want := []string{"search jam", "stats jam"}; !slices.Equal(got, want) {
		t.Fatalf("got %v", got)
	}
}

func TestErrorsAndPanicsReachThePublisher(t *testing.T) {
	b := NewBus()
	boom := errors.New("index down")
	ran := 0
	noteDeleted.Subscribe(b, func(context.Context, deleted) error { ran++; return boom })
	noteDeleted.Subscribe(b, func(context.Context, deleted) error { ran++; panic("nil map") })
	noteDeleted.Subscribe(b, func(context.Context, deleted) error { ran++; return nil })
	err := noteDeleted.Publish(context.Background(), b, deleted{})
	if !errors.Is(err, boom) || !strings.Contains(err.Error(), "panic: nil map") || ran != 3 {
		t.Fatalf("ran %d: %v", ran, err)
	}
}

func TestOneNameTwoTypesPanics(t *testing.T) {
	b := NewBus()
	noteDeleted.Subscribe(b, func(context.Context, deleted) error { return nil })
	other := Topic[int]{Name: "notes.deleted"}
	defer func() {
		if p := recover(); p == nil || !strings.Contains(p.(string), "carries events.deleted, not int") {
			t.Fatalf("got %v", p)
		}
	}()
	other.Subscribe(b, func(context.Context, int) error { return nil })
}

func TestConcurrentUse(t *testing.T) {
	b := NewBus()
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			noteDeleted.Subscribe(b, func(context.Context, deleted) error { return nil })
			_ = noteDeleted.Publish(context.Background(), b, deleted{})
		})
	}
	wg.Wait()
}
