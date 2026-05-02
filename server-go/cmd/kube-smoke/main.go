// Command kube-smoke is a tiny throwaway binary that exercises the
// internal/kube wrappers against a real cluster. Used to satisfy the
// Phase 1 acceptance step in kuso/docs/REWRITE.md:
//
//	"Test: list KusoEnvironments from the live Hetzner cluster."
//
// Not shipped in the server image. Run from a workstation with a kubeconfig
// pointed at the target cluster:
//
//	go run ./cmd/kube-smoke -namespace kuso
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"text/tabwriter"
	"time"

	"kuso/server/internal/kube"
)

func main() {
	namespace := flag.String("namespace", "kuso", "namespace to scan")
	timeout := flag.Duration("timeout", 10*time.Second, "request timeout")
	flag.Parse()

	c, err := kube.NewClient()
	if err != nil {
		log.Fatalf("kube client: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	envs, err := c.ListKusoEnvironments(ctx, *namespace)
	if err != nil {
		log.Fatalf("list envs: %v", err)
	}

	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tPROJECT\tSERVICE\tKIND\tBRANCH\tHOST\tSECRETS_REV")
	for _, e := range envs {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			e.Name, e.Spec.Project, e.Spec.Service, e.Spec.Kind, e.Spec.Branch, e.Spec.Host, e.Spec.SecretsRev,
		)
	}
	if err := tw.Flush(); err != nil {
		log.Fatalf("flush: %v", err)
	}
	fmt.Printf("\n%d KusoEnvironment(s) in %q\n", len(envs), *namespace)
}
