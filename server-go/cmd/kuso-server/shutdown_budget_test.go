package main

import (
	"os"
	"regexp"
	"strconv"
	"testing"
	"time"
)

// LOOP-19: preStop + HTTP shutdown + worker drain used to add up to 75s
// against a 60s grace period, so SIGKILL landed before the leader leases
// were released and the next pod waited out a full LeaseDuration.
func TestShutdownFitsTerminationGrace(t *testing.T) {
	raw, err := os.ReadFile("../../../deploy/server-go.yaml")
	if err != nil {
		t.Skipf("deploy manifest not found: %v", err)
	}
	grace := regexp.MustCompile(`terminationGracePeriodSeconds:\s*(\d+)`).FindSubmatch(raw)
	preStop := regexp.MustCompile(`preStop:\s*\n\s*exec:\s*\n\s*command: \["/bin/sh", "-c", "sleep (\d+)"\]`).FindSubmatch(raw)
	if grace == nil || preStop == nil {
		t.Fatal("could not find terminationGracePeriodSeconds / preStop sleep in deploy/server-go.yaml")
	}
	g, _ := strconv.Atoi(string(grace[1]))
	p, _ := strconv.Atoi(string(preStop[1]))
	used := time.Duration(p)*time.Second + httpShutdownTimeout + workerDrain + shutdownSettle
	// Leave room for the lease release and process exit after workers stop.
	if budget := time.Duration(g)*time.Second - 5*time.Second; used > budget {
		t.Errorf("shutdown can take %s (preStop %ds + http %s + drain %s + settle %s), over the %ds grace minus 5s",
			used, p, httpShutdownTimeout, workerDrain, shutdownSettle, g)
	}
}
