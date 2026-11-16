package m0

// Reading is one qualified time reading in Unix seconds. Confident is false
// when the time source cannot establish confidence; unknown time closes
// forward work and never delays an explicit authorized stop.
type Reading struct {
	Unix      int64
	Confident bool
}

// Clock is the qualified time and deadline source seam.
type Clock interface {
	Now() (Reading, error)
}

// UnpinnedClock refuses by name.
type UnpinnedClock struct{}

func (UnpinnedClock) Now() (Reading, error) { return Reading{}, Unpinned(SeamClock) }

// Deadlines are the original deadlines fixed at reservation. Expiry is the
// earlier of policy not_after and reservation plus max_runtime; DeadlineMs is
// Expiry in the executor-entry coordinate; the detect/stop bounds are copied
// from the policy so later policy edits cannot move them.
type Deadlines struct {
	ReservedAt    int64
	Expiry        int64
	DetectSeconds int64
	StopSeconds   int64
	DeadlineMs    int64
	SlackMs       int64
}

func originalDeadlines(p *Policy, now int64, q Qualified) (Deadlines, error) {
	expiry := now + p.Bounds.MaxRuntimeSeconds
	if p.NotAfter < expiry {
		expiry = p.NotAfter
	}
	if expiry <= now {
		return Deadlines{}, refuse(CodePolicyWindow, "no runtime remains before policy expiry")
	}
	return Deadlines{
		ReservedAt:    now,
		Expiry:        expiry,
		DetectSeconds: p.Bounds.DetectSeconds,
		StopSeconds:   p.Bounds.StopSeconds,
		DeadlineMs:    expiry * 1000,
		SlackMs:       q.SlackMs,
	}, nil
}

// effectiveNow applies the durable watermark: expiry is checked at
// max(now, watermark), so a clock step can only narrow authority.
func effectiveNow(now, watermark int64) int64 { return max(now, watermark) }

// windowOpen checks not_before at min(now, watermark) and not_after at
// max(now, watermark). Equality with not_after is outside the window. A zero
// watermark means none has been recorded yet and does not lower now.
func windowOpen(p *Policy, now, watermark int64) bool {
	low := now
	if watermark > 0 {
		low = min(now, watermark)
	}
	return low >= p.NotBefore && effectiveNow(now, watermark) < p.NotAfter
}

// startPermitted is the C rule: expiry or Rv before C refuses; C at expiry
// also refuses. Only strictly earlier time permits C.
func startPermitted(now, watermark, expiry int64) bool {
	return effectiveNow(now, watermark) < expiry
}

// StopCeiling is the earliest absolute completion deadline of one stop
// obligation: event + detect + stop (the M0 total), detection + stop (the
// detection component) and every outstanding earlier deadline (zero entries
// are ignored). Neither receipt arrival nor delayed polling resets it.
func StopCeiling(event, detection, detectSeconds, stopSeconds int64, outstanding ...int64) int64 {
	ceiling := event + detectSeconds + stopSeconds
	if c := detection + stopSeconds; c < ceiling {
		ceiling = c
	}
	for _, o := range outstanding {
		if o > 0 && o < ceiling {
			ceiling = o
		}
	}
	return ceiling
}

// MeetsCeiling reports whether completion at the given time satisfies the
// ceiling; equality meets it.
func MeetsCeiling(completion, ceiling int64) bool { return completion <= ceiling }

// DetectionTimely reports whether detection happened within detectSeconds of
// the original event.
func DetectionTimely(event, detection, detectSeconds int64) bool {
	return detection >= event && detection-event <= detectSeconds
}
