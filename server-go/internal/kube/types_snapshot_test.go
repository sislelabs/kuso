package kube

import (
	"encoding/json"
	"testing"
)

func TestSnapshotBeforeDeployRoundTrips(t *testing.T) {
	s := KusoServiceSpec{SnapshotBeforeDeploy: true}
	b, _ := json.Marshal(s)
	var back KusoServiceSpec
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	if !back.SnapshotBeforeDeploy {
		t.Fatalf("service snapshotBeforeDeploy lost in round-trip: %s", b)
	}
	e := KusoEnvironmentSpec{SnapshotBeforeDeploy: true}
	eb, _ := json.Marshal(e)
	var eBack KusoEnvironmentSpec
	if err := json.Unmarshal(eb, &eBack); err != nil {
		t.Fatal(err)
	}
	if !eBack.SnapshotBeforeDeploy {
		t.Fatalf("env snapshotBeforeDeploy lost in round-trip: %s", eb)
	}
}
