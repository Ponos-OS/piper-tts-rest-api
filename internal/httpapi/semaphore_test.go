package httpapi

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSemaphore_NeverExceedsSlots(t *testing.T) {
	const slots = 3
	sem := newSemaphore(slots)
	var current, highest atomic.Int32
	var wg sync.WaitGroup

	for range 20 {
		wg.Go(func() {
			require.NoError(t, sem.acquire(t.Context()))
			defer sem.release()

			now := current.Add(1)
			defer current.Add(-1)
			for {
				seen := highest.Load()
				if now <= seen || highest.CompareAndSwap(seen, now) {
					break
				}
			}
			time.Sleep(5 * time.Millisecond)
		})
	}
	wg.Wait()

	assert.EqualValues(t, slots, highest.Load())
}

func TestSemaphore_ReleaseLetsWaiterIn(t *testing.T) {
	sem := newSemaphore(1)
	require.NoError(t, sem.acquire(t.Context()))
	acquired := make(chan error, 1)

	go func() { acquired <- sem.acquire(t.Context()) }()

	assert.Never(t, func() bool { return len(acquired) > 0 }, 30*time.Millisecond, 5*time.Millisecond)
	sem.release()
	select {
	case err := <-acquired:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("waiter was not let in after release")
	}
}

func TestSemaphore_CancelledWaiterDoesNotTakeSlot(t *testing.T) {
	sem := newSemaphore(1)
	require.NoError(t, sem.acquire(t.Context()))
	waiting, cancel := context.WithTimeout(t.Context(), 10*time.Millisecond)
	defer cancel()

	err := sem.acquire(waiting)

	require.ErrorIs(t, err, context.DeadlineExceeded)
	sem.release()
	free, cancelFree := context.WithTimeout(t.Context(), time.Second)
	defer cancelFree()
	require.NoError(t, sem.acquire(free), "the slot leaked")
}
