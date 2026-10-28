// Package events lets one module tell others that something happened without
// knowing who listens: notes publishes NoteDeleted, search and stats subscribe.
//
// A topic is declared once, typed, in the package that publishes it:
//
//	var NoteDeleted = events.Topic[NoteDeletedEvent]{Name: "notes.deleted"}
//
// and the compiler holds both sides to its type:
//
//	notes.NoteDeleted.Subscribe(bus, func(ctx context.Context, e notes.NoteDeletedEvent) error { … })
//	err := NoteDeleted.Publish(ctx, bus, NoteDeletedEvent{Slug: slug})
//
// Publish runs every subscriber, in the order they subscribed, before it
// returns, with the publisher's ctx: a subscriber's error reaches the
// publisher, which can roll its transaction back. That is the point and the
// limit. Work that may be slow, or must survive a crash, does not belong in a
// subscriber: the subscriber enqueues a queue job instead, through the
// publisher's transaction (queue is an outbox), and the job does the work.
//
// A Bus is a value passed to the modules that use it, like a database: there is
// no global one.
package events

import (
	"context"
	"errors"
	"fmt"
	"sync"
)

// Topic is a kind of event, carrying a T.
type Topic[T any] struct {
	Name string
}

// Bus delivers events to their subscribers.
type Bus struct {
	mu   sync.RWMutex
	subs map[string]topicSubs
}

// topicSubs is one topic's subscribers, typed by the topic's event.
type topicSubs interface{ eventType() string }

type typedSubs[T any] struct {
	fns []func(ctx context.Context, event T) error
}

func (*typedSubs[T]) eventType() string { return fmt.Sprintf("%T", *new(T)) }

// NewBus is a bus with no subscribers.
func NewBus() *Bus { return &Bus{subs: map[string]topicSubs{}} }

// Subscribe adds fn to the topic's subscribers. It panics if the topic has no
// name, or if its name is used by a topic of another event type: two packages
// would be sending each other events neither can read.
func (t Topic[T]) Subscribe(b *Bus, fn func(ctx context.Context, event T) error) {
	if fn == nil {
		panic(fmt.Sprintf("events: topic %q: nil subscriber", t.Name))
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	subs := t.subs(b)
	if subs == nil {
		subs = &typedSubs[T]{}
		b.subs[t.Name] = subs
	}
	subs.fns = append(subs.fns, fn)
}

// Publish delivers event to every subscriber, in order, and returns their
// errors joined; one failing does not stop the next. A subscriber that panics
// fails with the panic as its error. With no subscriber it does nothing.
func (t Topic[T]) Publish(ctx context.Context, b *Bus, event T) error {
	b.mu.RLock()
	var fns []func(context.Context, T) error
	if subs := t.subs(b); subs != nil {
		fns = subs.fns
	}
	b.mu.RUnlock()
	var errs []error
	for i, fn := range fns {
		if err := deliver(ctx, fn, event); err != nil {
			errs = append(errs, fmt.Errorf("events: %s: subscriber %d: %w", t.Name, i+1, err))
		}
	}
	return errors.Join(errs...)
}

func deliver[T any](ctx context.Context, fn func(context.Context, T) error, event T) (err error) {
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("panic: %v", p)
		}
	}()
	return fn(ctx, event)
}

// subs is the topic's subscribers, or nil. It panics on a nameless topic, or a
// name taken by another event type. b.mu is held.
func (t Topic[T]) subs(b *Bus) *typedSubs[T] {
	if t.Name == "" {
		panic("events: a topic needs a name")
	}
	got, ok := b.subs[t.Name]
	if !ok {
		return nil
	}
	typed, ok := got.(*typedSubs[T])
	if !ok {
		panic(fmt.Sprintf("events: topic %q carries %s, not %s", t.Name, got.eventType(), (*typedSubs[T])(nil).eventType()))
	}
	return typed
}
