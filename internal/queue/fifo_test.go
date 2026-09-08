package queue

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"platform-gateway/internal/backend"
)

func TestFIFO_ThirdWaitsFourthBusy(t *testing.T) {
	q := New("cklogs", 2, 1, time.Second)
	started := make(chan struct{}, 8)
	release := make(chan struct{})
	block := func(ctx context.Context) error {
		started <- struct{}{}
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	var wg sync.WaitGroup
	wg.Add(2)
	for i := 0; i < 2; i++ {
		go func() {
			defer wg.Done()
			if err := q.Run(context.Background(), block); err != nil {
				t.Errorf("in-flight: %v", err)
			}
		}()
	}
	waitStarted(t, started, 2)

	var thirdStarted atomic.Bool
	thirdDone := make(chan error, 1)
	go func() {
		thirdDone <- q.Run(context.Background(), func(ctx context.Context) error {
			thirdStarted.Store(true)
			return block(ctx)
		})
	}()
	time.Sleep(40 * time.Millisecond)
	if thirdStarted.Load() {
		t.Fatal("third request should still be waiting")
	}

	errCh := make(chan error, 1)
	go func() {
		errCh <- q.Run(context.Background(), func(ctx context.Context) error { return nil })
	}()
	select {
	case err := <-errCh:
		var busy *backend.BusyError
		if err == nil {
			t.Fatal("fourth should be busy")
		}
		if !asBusy(err, &busy) {
			t.Fatalf("fourth err=%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("fourth did not return")
	}

	close(release)
	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := <-thirdDone; err != nil {
			t.Errorf("third: %v", err)
		}
	}()
	wg.Wait()
}

func TestFIFO_WaitTimeout(t *testing.T) {
	q := New("cklogs", 1, 1, 40*time.Millisecond)
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- q.Run(context.Background(), func(ctx context.Context) error {
			started <- struct{}{}
			select {
			case <-release:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		})
	}()
	waitStarted(t, started, 1)
	err := q.Run(context.Background(), func(ctx context.Context) error { return nil })
	if err == nil {
		t.Fatal("expected wait timeout")
	}
	if _, ok := err.(*backend.WaitTimeoutError); !ok {
		t.Fatalf("err=%v", err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Errorf("in-flight: %v", err)
	}
}

func asBusy(err error, dest **backend.BusyError) bool {
	e, ok := err.(*backend.BusyError)
	if ok {
		*dest = e
	}
	return ok
}

func waitStarted(t *testing.T, started chan struct{}, n int) {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for i := 0; i < n; i++ {
		select {
		case <-started:
		case <-deadline:
			t.Fatalf("only got %d started", i)
		}
	}
}
