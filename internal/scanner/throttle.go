package scanner

import (
	"context"
	"math"

	"golang.org/x/time/rate"
)

// Throttle limits metadata operations (one per readdir and one per lstat) per
// second across all workers. A nil Throttle does not limit. The rate can be
// changed while a scan runs, which is how the day/night profile works.
type Throttle struct {
	l *rate.Limiter
}

// NewThrottle returns a Throttle for opsPerSec; opsPerSec <= 0 means unlimited
// for now (the rate can be lowered later with SetRate).
func NewThrottle(opsPerSec float64) *Throttle {
	t := &Throttle{l: rate.NewLimiter(rate.Inf, 1)}
	t.SetRate(opsPerSec)
	return t
}

// SetRate changes the limit; opsPerSec <= 0 removes it.
func (t *Throttle) SetRate(opsPerSec float64) {
	if t == nil {
		return
	}
	if opsPerSec <= 0 {
		t.l.SetLimit(rate.Inf)
		t.l.SetBurst(math.MaxInt32)
		return
	}
	burst := int(opsPerSec)
	if burst < 1 {
		burst = 1
	}
	t.l.SetBurst(burst)
	t.l.SetLimit(rate.Limit(opsPerSec))
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
