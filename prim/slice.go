package prim

import (
	"context"
	"fmt"
	"runtime"
	"sync/atomic"

	"github.com/gostdlib/concurrency/goroutines"
	"github.com/gostdlib/concurrency/goroutines/limited"
	"github.com/gostdlib/internals/otel/span"
)

// Mutator is a function that takes in a value T and returns an element R.
// T and R can be the same type.
type Mutator[T, R any] func(context.Context, T) (R, error)

// Slice applies Mutator "m" to each element in "s" using the goroutines Pool
// "p". If p == nil, p becomes a limited.Pool using up to runtime.NumCPU().
// Errors will be returned, but will not stop this from completing.
// Values at the position that return an error will remain unchanged.
func Slice[T any](ctx context.Context, s []T, mut Mutator[T, T], p goroutines.Pool, subOpts ...goroutines.SubmitOption) error {
	spanner := span.Get(ctx)

	if len(s) == 0 {
		return nil
	}

	if p == nil {
		var err error
		p, err = limited.New("", runtime.NumCPU())
		if err != nil {
			spanner.Error(err)
			return err
		}
		defer p.Close()
	}

	ptr := atomic.Pointer[error]{}

	for i := 0; i < len(s); i++ {
		i := i

		if ctx.Err() != nil {
			return ctx.Err()
		}

		err := p.Submit(
			ctx,
			func(ctx context.Context) {
				var err error
				s[i], err = mut(ctx, s[i])
				if err != nil {
					applyErr(&ptr, err)
				}
			},
			subOpts...,
		)
		if err != nil {
			return err
		}
	}
	p.Wait()

	errPtr := ptr.Load()
	if errPtr != nil {
		spanner.Error(*errPtr)
		return *errPtr
	}
	return nil
}

// ResultSlice takes values in slice "s" and applies Mutator "m" to get a new result slice []R.
// Slice "s" is not mutated. This allows you to have a returns slice of a different type or
// simply to leave the passed slice untouched.
// Errors will be returned, but will not stop this from completing. Values at the
// position that return an error will be the zero value for the R type.
func ResultSlice[T, R any](ctx context.Context, s []T, mut Mutator[T, R], p goroutines.Pool, subOpts ...goroutines.SubmitOption) ([]R, error) {
	spanner := span.Get(ctx)

	if len(s) == 0 {
		if s == nil {
			return nil, nil
		}
		return []R{}, nil
	}

	if p == nil {
		var err error
		p, err = limited.New("", runtime.NumCPU())
		if err != nil {
			spanner.Error(err)
			return nil, err
		}
		defer p.Close()
	}

	ptr := atomic.Pointer[error]{}
	results := make([]R, len(s))
	for i := 0; i < len(s); i++ {
		i := i

		if ctx.Err() != nil {
			return nil, ctx.Err()
		}

		p.Submit(
			ctx,
			func(ctx context.Context) {
				var err error
				results[i], err = mut(ctx, s[i])
				if err != nil {
					applyErr(&ptr, err)
				}
			},
			subOpts...,
		)
	}
	p.Wait()

	errPtr := ptr.Load()
	if errPtr != nil {
		spanner.Error(*errPtr)
		return results, *errPtr
	}
	return results, nil
}

func applyErr(ptr *atomic.Pointer[error], err error) {
	for {
		existing := ptr.Load()
		if existing == nil {
			if ptr.CompareAndSwap(nil, &err) {
				return
			}
		} else {
			if err == context.Canceled {
				return
			}
			err = fmt.Errorf("%w", err)
			if ptr.CompareAndSwap(existing, &err) {
				return
			}
		}
	}
}
