package errorscan

import (
	"regexp"
	"strings"
	"time"
)

// A multi-line error (stack trace, Python traceback, a printf with
// embedded newlines) reaches LogLine as one row per line. Scanning rows
// one by one made every matching line its own error group, so a single
// crash showed up as two or three "errors". The grouper folds a head
// line plus the continuation lines written with it into one occurrence.

// contGap bounds the time between consecutive lines of one error. A
// trace is written in one go, so its lines share (almost) one kubelet
// timestamp; 100ms tolerates line-buffered writers without gluing
// unrelated later output onto an error.
const contGap = 100 * time.Millisecond

// sameWriteGap is the window for the "same write" rule below: lines
// stamped within it came out of one write call.
const sameWriteGap = time.Millisecond

const maxChainLines = 200

var (
	// Shapes that never start a log record of their own: indented
	// frames, JS/Java "at …", Python `File "…"`, Java "Caused by" /
	// "... N more", Go goroutine headers and "created by".
	contShapeRe = regexp.MustCompile(`^(?:\s+\S|at\s|File "|Caused by|\.\.\. \d+ more|goroutine \d+ \[|created by |\[signal |exit status \d+)`)
	// Go panic frames: "main.main()", "net/http.(*conn).serve(0xc0001)".
	goFrameRe = regexp.MustCompile(`^[\w./*()\[\]-]+\(.*\)(?:\s.*)?$`)
	// The exception line that closes a Python traceback: a (dotted) class
	// name, then ": message" or nothing. Not every exception class ends in
	// Error ("smtplib.SMTPServerDisconnected: …").
	pyFinalRe   = regexp.MustCompile(`^[A-Za-z_][\w.]*(?::\s|:?$)`)
	pyChainedRe = regexp.MustCompile(`^(?:During handling of the above exception|The above exception was the direct cause)`)
	tracebackRe = regexp.MustCompile(`(?i)Traceback \(most recent call last\)`)
)

type scanLine struct {
	id      int64
	ts      time.Time
	pod     string
	project string
	service string
	env     string
	line    string
}

// errorChain is one error occurrence being assembled.
type errorChain struct {
	head  scanLine
	lines []string
	last  time.Time
	// title is the line the group is named and fingerprinted after:
	// the head, or a Python traceback's closing exception line.
	title   string
	python  bool
	pyDone  bool
	goPanic bool
}

func (c *errorChain) raw() string {
	return strings.TrimRight(strings.Join(c.lines, "\n"), "\n ")
}

// grouper holds at most one open chain per pod.
type grouper struct {
	open map[string]*errorChain
	emit func(*errorChain)
}

func newGrouper(emit func(*errorChain)) *grouper {
	return &grouper{open: map[string]*errorChain{}, emit: emit}
}

// feed takes runtime lines in LogLine id order. isErr says whether the
// line matches an error pattern on its own.
func (g *grouper) feed(l scanLine, isErr bool) {
	if c := g.open[l.pod]; c != nil {
		if gap := l.ts.Sub(c.last); gap >= 0 && gap <= contGap && len(c.lines) < maxChainLines && c.continues(l.line, isErr, gap) {
			c.lines = append(c.lines, l.line)
			c.last = l.ts
			return
		}
		g.close(l.pod)
	}
	if !isErr {
		return
	}
	g.open[l.pod] = &errorChain{
		head:    l,
		lines:   []string{l.line},
		last:    l.ts,
		title:   l.line,
		python:  tracebackRe.MatchString(l.line),
		goPanic: strings.HasPrefix(l.line, "panic: "),
	}
}

func (g *grouper) close(pod string) {
	c := g.open[pod]
	delete(g.open, pod)
	if c != nil {
		g.emit(c)
	}
}

// flush emits every open chain (end of a scan batch).
func (g *grouper) flush() {
	for pod := range g.open {
		g.close(pod)
	}
}

func (c *errorChain) continues(line string, isErr bool, gap time.Duration) bool {
	if strings.TrimSpace(line) == "" {
		// Blank separators inside Go panics and chained Python tracebacks.
		return c.goPanic || c.python
	}
	// logging.exception writes "ERROR <msg>" and then the traceback; the
	// traceback belongs to that error, named after its final exception.
	if !c.python && tracebackRe.MatchString(line) {
		c.python = true
		c.pyDone = false
		return true
	}
	if c.python {
		if tracebackRe.MatchString(line) || pyChainedRe.MatchString(line) {
			c.pyDone = false
			return true
		}
		if !c.pyDone && pyFinalRe.MatchString(line) {
			c.pyDone = true
			c.title = line
			return true
		}
	}
	if contShapeRe.MatchString(line) {
		if strings.HasPrefix(line, "goroutine ") {
			c.goPanic = true
		}
		return true
	}
	if c.goPanic && goFrameRe.MatchString(line) {
		return true
	}
	// Same write, sibling line: "ERROR probe line one\nERROR probe line
	// two". A repeat of the same error (same fingerprint) is a second
	// occurrence, not a continuation.
	if isErr && gap <= sameWriteGap {
		prev := c.lines[len(c.lines)-1]
		if fingerprintFor(prev) != fingerprintFor(line) && sharesPrefix(prev, line) {
			return true
		}
	}
	return false
}

// sharesPrefix: a common prefix of at least 16 bytes covering at least
// half the shorter line.
func sharesPrefix(a, b string) bool {
	n := min(len(a), len(b))
	i := 0
	for i < n && a[i] == b[i] {
		i++
	}
	return i >= 16 && i*2 >= n
}
