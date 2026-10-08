// Package uptime pings every production web service over its in-cluster
// Service and tells notify when one goes down or recovers. See
// docs/superpowers/specs/2026-10-02-uptime-checks-design.md.
package uptime

import "time"

const (
	Interval      = 60 * time.Second
	ProbeTimeout  = 10 * time.Second
	DownAfter     = 3
	UpAfter       = 3
	Cooldown      = 30 * time.Minute
	StormProjects = 3
	// MaxHold caps how long a crash-loop or rollout can hold an outage
	// back. A surge pod stuck in CrashLoopBackOff can sit there for days
	// while the old pod serves; without a cap it would silence a later,
	// unrelated outage of the same env.
	MaxHold = 15 * time.Minute
)

type Outcome int

const (
	OutcomeOK Outcome = iota
	OutcomeFail
	// OutcomePaused: the target isn't serving on purpose (stopped,
	// asleep, no image).
	OutcomePaused
)

type Action int

const (
	ActionNone Action = iota
	ActionDown
	ActionRecovered
	// ActionClosed: an alerted outage ended because the target stopped
	// being checked (stopped, asleep, opted out, deleted), not because
	// it answered again.
	ActionClosed
)

// Hold says why a failing target hasn't alerted.
const (
	HoldConfirming = "confirming"
	HoldCrashLoop  = "crash-looping"
	HoldCooldown   = "cooldown"
	HoldRollingOut = "rolling-out"
)

// State is the per-target state carried between ticks. Zero times mean
// unset.
type State struct {
	FailStreak  int
	OkStreak    int
	DownSince   time.Time
	OkSince     time.Time
	Alerted     bool
	LastAlertAt time.Time
	Hold        string
}

type Decision struct {
	Action Action
	// DownSince is the open outage's first failed check (ActionDown) or
	// the closed outage's (ActionRecovered).
	DownSince time.Time
	// DownFor is set on ActionRecovered.
	DownFor time.Duration
}

// Signals are the per-target facts that can hold a failing check back.
type Signals struct {
	// PodsBad: pods are crash-looping or can't pull their image, which
	// pod.crashed already alerts on.
	PodsBad bool
	// RollingOut: a deploy, wake or first start is still bringing pods
	// up, so failing checks are expected.
	RollingOut bool
}

// Decide advances one target by one check.
func Decide(s State, o Outcome, sig Signals, now time.Time) (State, Decision) {
	switch o {
	case OutcomePaused:
		var d Decision
		if s.Alerted {
			// A page went out for this outage: close it out loud so the
			// channel isn't left with an unanswered "down".
			d = Decision{Action: ActionClosed, DownSince: s.DownSince, DownFor: now.Sub(s.DownSince)}
		}
		return State{LastAlertAt: s.LastAlertAt}, d

	case OutcomeFail:
		s.FailStreak++
		s.OkStreak = 0
		s.OkSince = time.Time{}
		if s.DownSince.IsZero() {
			s.DownSince = now
		}
		if s.Alerted {
			s.Hold = ""
			return s, Decision{}
		}
		holdable := now.Sub(s.DownSince) < MaxHold
		switch {
		case s.FailStreak < DownAfter:
			s.Hold = HoldConfirming
		case sig.RollingOut && holdable:
			s.Hold = HoldRollingOut
		case sig.PodsBad && holdable:
			s.Hold = HoldCrashLoop
		case !s.LastAlertAt.IsZero() && now.Sub(s.LastAlertAt) < Cooldown:
			s.Hold = HoldCooldown
		default:
			s.Hold = ""
			s.Alerted = true
			s.LastAlertAt = now
			return s, Decision{Action: ActionDown, DownSince: s.DownSince}
		}
		return s, Decision{}

	default: // OutcomeOK
		s.OkStreak++
		s.FailStreak = 0
		if s.OkStreak == 1 {
			s.OkSince = now
		}
		if !s.Alerted {
			// Nothing was sent for this outage, so nothing waits on a
			// stable recovery: close it on the first good check. Keeping
			// it open would leave a stale DownSince on a target that
			// fails now and then.
			s.DownSince = time.Time{}
			s.Hold = ""
			return s, Decision{}
		}
		if s.OkStreak < UpAfter {
			return s, Decision{}
		}
		d := Decision{Action: ActionRecovered, DownSince: s.DownSince, DownFor: s.OkSince.Sub(s.DownSince)}
		s.DownSince = time.Time{}
		s.Alerted = false
		return s, d
	}
}
