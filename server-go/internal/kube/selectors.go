package kube

import (
	"k8s.io/apimachinery/pkg/labels"
)

// Standard label keys used across the codebase. Promoted here so the
// kube/projects/addons/builds/secrets packages all reach for one set
// of constants instead of re-typing the strings.
const (
	LabelProject = "kuso.sislelabs.com/project"
	LabelService = "kuso.sislelabs.com/service"
	LabelEnv     = "kuso.sislelabs.com/env"
)

// LabelSelector builds a properly-formatted kube label selector
// string from pairs, escaping anything that could be interpreted as
// selector syntax. Going through labels.SelectorFromSet (which
// validates each value via labels.IsValidLabelValue under the hood)
// prevents the class of bug where a project name like "foo," would
// be appended into a selector string via string concatenation and
// re-shape the query at the apiserver.
//
// Pairs with empty values are dropped. An empty map returns "" —
// meaning "no selector" to ListOptions, which selects everything.
// Callers that need "everything" should pass an empty map
// deliberately; the more common case is to error out before reaching
// this function if any value is empty.
func LabelSelector(pairs map[string]string) string {
	clean := make(labels.Set, len(pairs))
	for k, v := range pairs {
		if v == "" {
			continue
		}
		clean[k] = v
	}
	return labels.SelectorFromSet(clean).String()
}
