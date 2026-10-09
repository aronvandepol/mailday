package syncd

import (
	"testing"
	"time"
)

func TestIdlePolicy(t *testing.T) {
	const max = 30 * time.Second
	sec := func(n float64) time.Duration { return time.Duration(n * float64(time.Second)) }

	t.Run("one random drop does not shrink", func(t *testing.T) {
		p := newIdlePolicy(max)
		if p.dropped(sec(4)) || p.interval() != max {
			t.Fatalf("interval = %s", p.interval())
		}
	})
	t.Run("two unrelated drops do not shrink", func(t *testing.T) {
		p := newIdlePolicy(max)
		p.dropped(sec(4))
		if p.dropped(sec(25)) || p.interval() != max {
			t.Fatalf("interval = %s", p.interval())
		}
	})
	t.Run("a healthy round between drops clears them", func(t *testing.T) {
		p := newIdlePolicy(max)
		p.dropped(sec(10))
		p.healthyRound()
		if p.dropped(sec(10)) || p.interval() != max {
			t.Fatalf("interval = %s", p.interval())
		}
	})
	t.Run("a drop after the timer would have fired is not early", func(t *testing.T) {
		p := newIdlePolicy(max)
		p.dropped(sec(30))
		if p.dropped(sec(30)) || p.interval() != max {
			t.Fatalf("interval = %s", p.interval())
		}
	})
	t.Run("a server that drops idles at 10 s shrinks it to 7 s", func(t *testing.T) {
		p := newIdlePolicy(max)
		if p.dropped(sec(10)) {
			t.Fatal("shrank on the first drop")
		}
		if !p.dropped(sec(10)) || p.interval() != sec(7) {
			t.Fatalf("interval = %s", p.interval())
		}
	})
	t.Run("the floor", func(t *testing.T) {
		p := newIdlePolicy(max)
		p.dropped(sec(1))
		p.dropped(sec(1))
		if p.interval() != minRefresh {
			t.Fatalf("interval = %s", p.interval())
		}
	})
	t.Run("grows by half per ten healthy rounds, up to the maximum, and resets", func(t *testing.T) {
		p := newIdlePolicy(max)
		p.dropped(sec(10))
		p.dropped(sec(10))
		if p.interval() != sec(7) {
			t.Fatalf("interval = %s", p.interval())
		}
		for range 9 {
			p.healthyRound()
		}
		if p.interval() != sec(7) {
			t.Fatalf("grew after 9 rounds: %s", p.interval())
		}
		// Not past the 10 s the server was seen to allow, until that is
		// forgotten after ceilingRounds healthy rounds.
		for range ceilingRounds - 10 {
			p.healthyRound()
		}
		if p.interval() != sec(7) {
			t.Fatalf("grew past the server's limit: %s", p.interval())
		}
		for range 10 {
			p.healthyRound()
		}
		if p.interval() != sec(10.5) {
			t.Fatalf("after the limit is forgotten: %s", p.interval())
		}
		for range 100 {
			p.healthyRound()
		}
		if p.interval() != max {
			t.Fatalf("after many rounds: %s, want the maximum", p.interval())
		}
		p.dropped(sec(10))
		p.dropped(sec(10))
		p.reset()
		if p.interval() != max {
			t.Fatalf("after reset: %s", p.interval())
		}
	})
	t.Run("an early drop of a grown interval is still seen as early", func(t *testing.T) {
		p := newIdlePolicy(max)
		p.dropped(sec(10))
		p.dropped(sec(10))
		for range ceilingRounds {
			p.healthyRound()
		}
		p.dropped(sec(10))
		if !p.dropped(sec(10)) || p.interval() != sec(7) {
			t.Fatalf("interval = %s", p.interval())
		}
	})
}

func TestIdlePolicyDoesNotGrowBackPastTheServersLimit(t *testing.T) {
	policy := newIdlePolicy(25 * time.Minute)
	// A host that hangs up 10 s into IDLE, twice.
	policy.dropped(10 * time.Second)
	if !policy.dropped(10 * time.Second) {
		t.Fatal("two drops at the same age should shrink the interval")
	}
	if policy.interval() != 7*time.Second {
		t.Fatalf("interval %s, want 7s", policy.interval())
	}
	// An hour of healthy rounds must not take it to 10.5 s, where it dies.
	for range ceilingRounds - 1 {
		policy.healthyRound()
	}
	if policy.interval() != 7*time.Second {
		t.Fatalf("interval grew to %s, past the 10 s the server allows", policy.interval())
	}
	// A new network forgets it.
	policy.reset()
	if policy.interval() != 25*time.Minute {
		t.Fatalf("after reset %s", policy.interval())
	}
}
