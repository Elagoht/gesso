package queue

import (
	"context"

	"github.com/Elagoht/gesso/db"
	"github.com/Elagoht/gesso/events"
)

// Publish sends ev on topic through the queue — to other processes, and past
// a crash. It is enqueued through d, the publisher's transaction: the event
// exists if and only if the data it is about was committed. A worker that
// Delivers the topic hands it on.
//
//	err := db.InTx(ctx, pool, func(tx db.DB) error {
//		… delete the note through tx …
//		_, err := queue.Publish(ctx, q, tx, notes.NoteDeleted, notes.NoteDeletedEvent{Slug: slug})
//		return err
//	})
func Publish[T any](ctx context.Context, q Queue, d db.DB, topic events.Topic[T], ev T) (int64, error) {
	return eventKind(topic).Enqueue(ctx, q, d, ev)
}

// Deliver publishes, on bus in w's process, every event Published on topic
// through the queue. Delivery is at least once: a subscriber that fails makes
// the event a job that failed, tried again with backoff — and its subscribers
// run again, so they must be idempotent. Workers that Deliver one topic share
// its events, each going to one of them: a service that wants every event
// delivers from a queue of its own.
func Deliver[T any](w *Worker, bus *events.Bus, topic events.Topic[T]) {
	Handle(w, eventKind(topic), func(ctx context.Context, ev T) error {
		return topic.Publish(ctx, bus, ev)
	})
}

func eventKind[T any](topic events.Topic[T]) Kind[T] { return Kind[T]{Name: "event:" + topic.Name} }
