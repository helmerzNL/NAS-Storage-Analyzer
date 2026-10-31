package scanner

import (
	"context"

	"golang.org/x/time/rate"
)

// Throttle limits metadata operations (one per readdir and one per lstat) per
// second across all workers. A nil Throttle does not limit.
type Throttle struct {
	l *rate.Limiter
}

// NewThrottle returns a Throttle for opsPerSec, or nil when opsPerSec <= 0.
func NewThrottle(opsPerSec float64) *Throttle {
	if opsPerSec <= 0 {
		return nil
	}
	burst := int(opsPerSec)
	if burst < 1 {
		burst = 1
	}
	return &Throttle{l: rate.NewLimiter(rate.Limit(opsPerSec), burst)}
}

// Wait blocks until n operations are allowed or ctx ends.
func (t *Throttle) Wait(ctx context.Context, n int) error {
	if t == nil {
		return ctx.Err()
	}
	for n > 0 {
		k := n
		if b := t.l.Burst(); k > b {
			k = b
		}
		if err := t.l.WaitN(ctx, k); err != nil {
			return err
		}
		n -= k
	}
	return nil
}
