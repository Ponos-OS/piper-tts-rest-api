package httpapi

import "context" // Standard way to carry cancellation signals, deadlines, and request-scoped values across function calls.

// semaphore caps how many callers hold a slot at once.
// Signal-only value: something happened, but there's no payload.
// The buffered channel is the slot pool: a send takes a slot, a receive frees one.
// Channels are Go's built-in way to communicate between goroutines. You can send into a channel and receive from it.
type semaphore chan struct{}

func newSemaphore(slots int) semaphore {
	// buffered channel of capacity slots can hold up to slots values without a receiver being ready.
	// Once it's full, any further send blocks until someone receives.
	return make(semaphore, slots)
}

// acquire blocks until a slot is free or ctx is done.
// Every successful acquire must be paired with a release, usually via defer.
func (s semaphore) acquire(ctx context.Context) error {
	select {
	case s <- struct{}{}: // A send operation: tries to put an empty struct into the channel.
		return nil // If there's room in the buffer → the send succeeds immediately, and we return nil (success).
	case <-ctx.Done(): // A receive operation: channel is closed when the context is cancelled or its deadline expires.
		return ctx.Err() // If the context gets cancelled while we're waiting for a slot, this case fires.
	}
}

// release discards the value.
// Receiving from the channel frees up a slot in the buffer.
func (s semaphore) release() {
	<-s
}
