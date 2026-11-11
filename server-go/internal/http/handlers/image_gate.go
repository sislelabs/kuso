package handlers

import (
	"context"
	"net/http"
	"strings"

	"kuso/server/internal/imagecheck"
	"kuso/server/internal/projects"
)

// refuseMissingImage gates an image change on the registry: when the new
// repository:tag is definitively absent it writes 422 and returns true.
// Without it a typo'd tag rolled the env onto ImagePullBackOff; on an
// RWO-volume (Recreate) env or a sleeping env there is no old pod left
// serving, so that was a full outage until someone reverted.
//
// Only a registry's explicit "no such tag" refuses. Private images,
// unreachable registries and anything else the anonymous check can't
// judge pass through, as does ?skipImageCheck=true.
func (h *ProjectsHandler) refuseMissingImage(ctx context.Context, w http.ResponseWriter, r *http.Request, project, service string, img *projects.ServiceImageSpec) bool {
	if img == nil || strings.TrimSpace(img.Repository) == "" || r.URL.Query().Get("skipImageCheck") == "true" {
		return false
	}
	if img.PullSecret != nil && strings.TrimSpace(*img.PullSecret) != "" {
		return false
	}
	ref, err := imagecheck.ParseRef(img.Repository, img.Tag)
	if err != nil {
		return false // PatchService's own validation answers this
	}
	if svc, gerr := h.Svc.GetService(ctx, project, service); gerr == nil && svc.Spec.Image != nil {
		cur := svc.Spec.Image
		if img.PullSecret == nil && cur.PullSecret != "" {
			return false // keeps its credential: checked when the pods pull
		}
		if curRef, perr := imagecheck.ParseRef(cur.Repository, cur.Tag); perr == nil && curRef == ref {
			return false // unchanged image: nothing new to verify
		}
	}
	res := h.imageChecker().Check(ctx, ref)
	if res.Status != imagecheck.NotFound {
		return false
	}
	writeErrCode(w, http.StatusUnprocessableEntity,
		"image not found: "+res.Detail+" (nothing was changed; pass ?skipImageCheck=true to set it anyway)",
		"image_not_found")
	return true
}
