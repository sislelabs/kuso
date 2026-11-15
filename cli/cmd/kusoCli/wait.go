package kusoCli

import (
	"fmt"
	"strings"
	"time"

	"github.com/go-resty/resty/v2"
)

// poller drives every "block until X finishes" loop in the CLI (build
// --follow / build wait, upgrade / upgrade status --wait). The server
// restarts during an upgrade and the ingress returns 5xx while it does,
// so a single failed request must not end the wait; a run of them, a
// 4xx, or an HTML error page must.
type poller struct {
	interval time.Duration
	timeout  time.Duration
	// maxTransient is how many consecutive transport errors / 5xx are
	// tolerated before giving up.
	maxTransient int
	sleep        func(time.Duration)
	now          func() time.Time
}

// pollSleep and pollNow are swapped by tests so waits run instantly.
var (
	pollSleep = time.Sleep
	pollNow   = time.Now
)

func newPoller(interval, timeout time.Duration, maxTransient int) poller {
	return poller{interval: interval, timeout: timeout, maxTransient: maxTransient, sleep: pollSleep, now: pollNow}
}

// errPollTimeout is returned (wrapped) when the deadline passes before
// check reports done.
var errPollTimeout = fmt.Errorf("timed out")

// run calls fetch every interval and hands each usable body to check
// until check reports done or returns an error. The first fetch happens
// immediately so an already-finished target returns at once.
func (p poller) run(what string, fetch func() (*resty.Response, error), check func(body []byte) (done bool, err error)) error {
	deadline := p.now().Add(p.timeout)
	failures := 0
	for first := true; ; first = false {
		if !first {
			if !p.now().Before(deadline) {
				return fmt.Errorf("%s: %w after %s", what, errPollTimeout, p.timeout)
			}
			p.sleep(p.interval)
		}
		resp, err := fetch()
		retry, verr := pollVerdict(what, resp, err)
		if verr != nil {
			if !retry {
				return verr
			}
			failures++
			if failures >= p.maxTransient {
				return fmt.Errorf("%s: %d requests in a row failed, last: %w", what, failures, verr)
			}
			continue
		}
		failures = 0
		done, cerr := check(resp.Body())
		if cerr != nil {
			return cerr
		}
		if done {
			return nil
		}
	}
}

// pollVerdict classifies one poll response. A nil error means the body is
// usable. Transport errors and 5xx are transient (retry=true); other
// non-2xx statuses and a 2xx HTML page (wrong URL, ingress error page)
// are fatal.
func pollVerdict(what string, resp *resty.Response, err error) (retry bool, verdict error) {
	if err != nil {
		return true, err
	}
	if code := resp.StatusCode(); code >= 500 {
		return true, fmt.Errorf("%s: HTTP %d", what, code)
	}
	if resp.StatusCode() >= 300 {
		return false, fmt.Errorf("%s: %w", what, checkRespErr(resp, nil))
	}
	if ct := resp.Header().Get("Content-Type"); strings.HasPrefix(ct, "text/html") {
		return false, fmt.Errorf("%s: server returned HTML (Content-Type=%s) — bailing", what, ct)
	}
	return false, nil
}
