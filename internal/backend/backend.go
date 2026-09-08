package backend

import (
	"context"
	"time"
)

// Gate limits one upstream backend.
type Gate interface {
	Name() string
	Run(ctx context.Context, op Operation) error
}

// Operation is one user-facing call. All upstream HTTP inside it must be serial.
type Operation func(ctx context.Context) error

type BusyError struct {
	RetryAfter time.Duration
}

func (e *BusyError) Error() string { return "gateway_busy" }

func (e *BusyError) Code() string { return "gateway_busy" }

type WaitTimeoutError struct{}

func (e *WaitTimeoutError) Error() string { return "gateway_queue_timeout" }

func (e *WaitTimeoutError) Code() string { return "gateway_queue_timeout" }
