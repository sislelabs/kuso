// Public reviewer-page endpoints (v0.17.0 Phase 2).
//
// These routes are deliberately UNAUTHENTICATED. The opaque token in
// the URL is the only credential: anyone with the link can view the
// preview's service URLs + record a decision. That's the design
// goal — non-technical reviewers shouldn't have to create kuso
// accounts to approve a PR.
//
// To bound the blast radius:
//   - read endpoint redacts every internal-only field (DB row IDs,
//     seed errors that might leak stack traces, ...)
//   - decision endpoint accepts only the three valid verbs +
//     enforces a one-write rule (re-submitting flips the decision
//     but only updates one column-set, no history table)
//   - rate limit: both routes ride the shared per-IP limiter
//     (RateLimitedReview in ratelimit.go), the same bucket that gates
//     login / invite / OAuth-start, so the opaque token can't be
//     brute-forced at full request rate

package handlers

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"kuso/server/internal/db"
	"kuso/server/internal/kube"
)

type PreviewReviewHandler struct {
	DB   *db.DB
	Kube *kube.Client
}

// PublicReviewerView is the shape the reviewer page consumes. Strips
// every DB-internal field; only what the reviewer needs to make a
// decision lands here.
type PublicReviewerView struct {
	Project         string                  `json:"project"`
	PRNumber        int                     `json:"prNumber"`
	PRTitle         string                  `json:"prTitle"`
	PRBody          string                  `json:"prBody"`
	PRAuthor        string                  `json:"prAuthor"`
	BaseRef         string                  `json:"baseRef"`
	HeadRef         string                  `json:"headRef"`
	Services        []PublicReviewerService `json:"services"`
	SeedPhase       string                  `json:"seedPhase"`
	SeedError       string                  `json:"seedError,omitempty"`
	Decision        string                  `json:"decision"` // "" until reviewer picks
	DecisionComment string                  `json:"decisionComment,omitempty"`
	DecidedAt       *time.Time              `json:"decidedAt,omitempty"`
	DecidedBy       string                  `json:"decidedBy,omitempty"`
	Closed          bool                    `json:"closed"`
}

type PublicReviewerService struct {
	Service string `json:"service"` // short name (e.g. "frontend")
	URL     string `json:"url"`     // public https URL of the preview pod
}

func (h *PreviewReviewHandler) Mount(r chi.Router) {
	// Public — no auth middleware in front. The router setup needs to
	// register this path BEFORE the auth-gate so reviewers can hit it
	// without a kuso login. See router.go for the mount order.
	//
	// Both routes ride the shared per-IP limiter (RateLimitedReview):
	// the 32-char token is the only credential, so without a cap an
	// attacker could brute-force the token space at full request rate
	// (the package comment claimed this limit existed before it did).
	r.Get("/api/reviews/{token}", RateLimitedReview(h.GetByToken))
	r.Post("/api/reviews/{token}/decision", RateLimitedReview(h.PostDecision))
}

// GetByToken returns the reviewer view for the given token. 404 on
// not-found (don't disclose existence of any other token).
func (h *PreviewReviewHandler) GetByToken(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	token := strings.TrimSpace(chi.URLParam(r, "token"))
	if len(token) < 32 {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	review, err := h.DB.GetPreviewReviewByToken(ctx, token)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	view := PublicReviewerView{
		Project:         review.Project,
		PRNumber:        review.PRNumber,
		PRTitle:         review.PRTitle,
		PRBody:          review.PRBody,
		PRAuthor:        review.PRAuthor,
		BaseRef:         review.BaseRef,
		HeadRef:         review.HeadRef,
		Decision:        review.Decision,
		DecisionComment: review.DecisionComment,
		DecidedAt:       review.DecidedAt,
		DecidedBy:       review.DecidedBy,
		Closed:          review.ClosedAt != nil,
	}
	// Enrich with live service URLs + seed phase. Walks the env CRs
	// labeled with this PR. Best-effort: kube unreachable returns the
	// view without services rather than erroring (reviewer page
	// already shows enough PR meta to start the conversation).
	if h.Kube != nil {
		envs, _ := h.Kube.ListKusoEnvironmentsByLabels(ctx, "kuso", map[string]string{
			"kuso.sislelabs.com/project": review.Project,
			"kuso.sislelabs.com/env":     "preview-pr-" + itoa(review.PRNumber),
		})
		for i := range envs {
			env := &envs[i]
			// Only services flagged reviewUrl=true land in the public
			// view. Need to fetch the parent service CR to check the
			// flag — cheap enough at the rare-frequency this page is
			// hit.
			svc, err := h.Kube.GetKusoService(ctx, "kuso", env.Spec.Service)
			if err != nil || svc == nil {
				continue
			}
			if svc.Spec.Previews == nil || !svc.Spec.Previews.ReviewURL {
				continue
			}
			if env.Spec.Host == "" {
				continue
			}
			scheme := "https"
			if !env.Spec.TLSEnabled {
				scheme = "http"
			}
			short := env.Spec.Service
			if i := strings.Index(short, "-"); i >= 0 && strings.HasPrefix(short, review.Project+"-") {
				short = short[len(review.Project)+1:]
			}
			view.Services = append(view.Services, PublicReviewerService{
				Service: short,
				URL:     scheme + "://" + env.Spec.Host,
			})
			// Pull seed phase from whichever env carries the
			// annotation. All envs sharing the same PR should agree;
			// last wins on mismatch.
			if anns := env.Annotations; anns != nil {
				if p := anns["kuso.sislelabs.com/preview-seed-phase"]; p != "" {
					view.SeedPhase = p
				}
				if e := anns["kuso.sislelabs.com/preview-seed-error"]; e != "" {
					view.SeedError = e
				}
			}
		}
	}
	writeJSON(w, http.StatusOK, view)
}

// PostDecision records the reviewer's verdict + optional comment.
// Body: { "decision": "approved"|"changes_requested"|"denied",
//
//	"comment": "...", "reviewer": "email@..." }
//
// 404 on token miss, 400 on bad decision verb, 200 + the updated view on success.
func (h *PreviewReviewHandler) PostDecision(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	token := strings.TrimSpace(chi.URLParam(r, "token"))
	if len(token) < 32 {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	var body struct {
		Decision string `json:"decision"`
		Comment  string `json:"comment"`
		Reviewer string `json:"reviewer"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	reviewer := strings.TrimSpace(body.Reviewer)
	if reviewer == "" {
		reviewer = "anonymous"
	}
	// Cap the unauthenticated free-text fields so a token holder can't
	// write megabytes per row (the global 1 MiB body limit is far too
	// loose for two short strings). Truncate rather than reject — the
	// reviewer's intent survives, storage abuse doesn't.
	const (
		maxComment  = 4096
		maxReviewer = 254 // RFC 5321 max email length
	)
	comment := body.Comment
	if len(comment) > maxComment {
		comment = comment[:maxComment]
	}
	if len(reviewer) > maxReviewer {
		reviewer = reviewer[:maxReviewer]
	}
	if err := h.DB.SetPreviewReviewDecision(ctx, token, body.Decision, comment, reviewer); err != nil {
		switch {
		case errors.Is(err, sql.ErrNoRows):
			http.Error(w, "not found", http.StatusNotFound)
		case errors.Is(err, db.ErrReviewClosed):
			http.Error(w, "review is closed", http.StatusConflict)
		case errors.Is(err, db.ErrInvalidDecision):
			http.Error(w, "invalid decision", http.StatusBadRequest)
		default:
			// Unexpected DB error — log server-side, never leak the SQL
			// text to this unauthenticated caller.
			slog.Default().Error("preview review: set decision", "err", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
		}
		return
	}
	// Re-render the view so the reviewer page can re-baseline without
	// a second fetch.
	h.GetByToken(w, r)
}

// itoa is a tiny strconv shim so we don't pull strconv into the
// reviewer-page handler when we only need int → string for one
// label lookup.
func itoa(n int) string {
	const digits = "0123456789"
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	pos := len(buf)
	for n > 0 {
		pos--
		buf[pos] = digits[n%10]
		n /= 10
	}
	if neg {
		pos--
		buf[pos] = '-'
	}
	return string(buf[pos:])
}
