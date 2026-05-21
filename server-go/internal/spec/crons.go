package spec

import (
	"strings"

	"kuso/server/internal/crons"
	"kuso/server/internal/kube"
)

// cronCreateReq maps a kuso.yaml CronSpec to the crons domain create
// request. kind=command carries an image (repo:tag split from the
// flat "image" string); kind=http carries a URL; kind=service carries
// no image (the cron reuses the named service's build image).
func cronCreateReq(c CronSpec) crons.CreateProjectCronRequest {
	req := crons.CreateProjectCronRequest{
		Name:     c.Name,
		Kind:     c.Kind,
		Schedule: c.Schedule,
		URL:      c.URL,
		Command:  c.Command,
		Suspend:  c.Suspend,
	}
	if c.Kind == "command" && c.Image != "" {
		repo, tag := splitImage(c.Image)
		req.Image = &kube.KusoImage{Repository: repo, Tag: tag}
	}
	return req
}

// cronUpdateReq maps a CronSpec to the partial update request. All
// pointer fields are set so apply is declarative (omitted YAML field
// → reset to default).
func cronUpdateReq(c CronSpec) crons.UpdateProjectCronRequest {
	sched := c.Schedule
	susp := c.Suspend
	return crons.UpdateProjectCronRequest{
		Schedule: &sched,
		Command:  c.Command,
		Suspend:  &susp,
	}
}

// splitImage splits "repo:tag" into its parts. A missing tag defaults
// to "latest". A repo with a registry-host colon (host:port/path) is
// handled by splitting on the LAST colon only when it follows a slash
// or there is no slash.
func splitImage(image string) (repo, tag string) {
	if i := strings.LastIndexByte(image, ':'); i >= 0 && !strings.ContainsRune(image[i:], '/') {
		return image[:i], image[i+1:]
	}
	return image, "latest"
}
