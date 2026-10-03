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
)

// Hold says why a failing target hasn't alerted.
const (
	HoldConfirming = "confirming"
	HoldCrashLoop  = "crash-looping"
	HoldCooldown   = "cooldown"
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

// Decide advances one target by one check. podsBad reports that the
// target's pods are crash-looping or can't pull their image, which
// pod.crashed already alerts on.
func Decide(s State, o Outcome, podsBad bool, now time.Time) (State, Decision) {
	switch o {
	case OutcomePaused:
		return State{LastAlertAt: s.LastAlertAt}, Decision{}

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
		switch {
		case s.FailStreak < DownAfter:
			s.Hold = HoldConfirming
		case podsBad:
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
