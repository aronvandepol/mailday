package syncd

import "time"

const (
	minRefresh = 3 * time.Second
	// dropTolerance is how far apart two early drops may be and still count
	// as one server habit rather than two unrelated network hiccups.
	dropTolerance = 3 * time.Second
	// earlyMargin separates "the server hung up before our timer" from timer
	// jitter.
	earlyMargin = 250 * time.Millisecond
	// healthyRoundsToGrow is how many clean IDLE rounds earn a longer interval.
	healthyRoundsToGrow = 10
	// ceilingRounds is how many healthy rounds forget a server's limit
	// (about two hours at 7 s): a network that misbehaved once is tried at
	// longer intervals again, a server that hangs up early costs one drop then.
	ceilingRounds = 1000
)

// idlePolicy decides how long IDLE runs before the watcher interrupts it for a
// NOOP. It shrinks only for a server that consistently drops idle connections
// (some hang up after 10 seconds): two drops in a row, no healthy round
// between them, at about the same age. One random disconnect, a Wi-Fi roam or
// a server restart does not qualify, and the interval grows back by half
// after every ten healthy rounds, up to the configured Options.Refresh.
type idlePolicy struct {
	max, current time.Duration
	earlyDrops   int
	lastEarly    time.Duration
	healthy      int
	// ceiling is the age at which this server was seen to hang up. Growing
	// back past it only walks into the next drop: a 10 s limit
	// turned 7 s into 10.5 s after ten good rounds, and the watch died every
	// few minutes for good.
	ceiling time.Duration
}

func newIdlePolicy(max time.Duration) idlePolicy {
	return idlePolicy{max: max, current: max}
}

func (p *idlePolicy) interval() time.Duration { return p.current }

// dropped notes that the server closed the connection lived after IDLE
// started, and reports whether the interval shrank.
func (p *idlePolicy) dropped(lived time.Duration) (shrunk bool) {
	p.healthy = 0
	if lived+earlyMargin >= p.current {
		p.earlyDrops = 0 // it would have been interrupted by then anyway
		return false
	}
	if p.earlyDrops > 0 && absDuration(lived-p.lastEarly) <= dropTolerance {
		p.earlyDrops++
	} else {
		p.earlyDrops = 1
	}
	p.lastEarly = lived
	if p.earlyDrops < 2 {
		return false
	}
	p.earlyDrops = 0
	p.ceiling = lived
	p.current = min(p.current, max(lived*7/10, minRefresh))
	return true
}

// healthyRound notes an IDLE round that ended with a NOOP answered.
func (p *idlePolicy) healthyRound() {
	p.earlyDrops = 0
	p.healthy++
	if p.ceiling > 0 && p.healthy >= ceilingRounds {
		p.ceiling = 0
	}
	if p.healthy%healthyRoundsToGrow == 0 && p.current < p.max {
		limit := p.max
		if p.ceiling > 0 {
			limit = min(limit, max(p.ceiling*7/10, minRefresh))
		}
		p.current = max(min(p.current*3/2, limit), p.current)
	}
}

// reset forgets what was learned, e.g. after a suspend and a new network.
func (p *idlePolicy) reset() {
	*p = newIdlePolicy(p.max)
}

func absDuration(d time.Duration) time.Duration {
	if d < 0 {
		return -d
	}
	return d
}
