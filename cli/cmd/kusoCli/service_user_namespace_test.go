package kusoCli

import (
	"encoding/json"
	"testing"
)

func TestParseUserNamespaceFlag(t *testing.T) {
	on, err := parseUserNamespaceFlag("on")
	if err != nil || on == nil || *on {
		t.Fatalf("on = %v, %v; want hostUsers false", on, err)
	}
	for _, v := range []string{"off", "default"} {
		if got, err := parseUserNamespaceFlag(v); err != nil || got != nil {
			t.Errorf("%s = %v, %v; want nil", v, got, err)
		}
	}
	if _, err := parseUserNamespaceFlag("maybe"); err == nil {
		t.Error("maybe: want error")
	}
}

// Setting one securityContext flag replaces the whole block server-side,
// so the fetched hostUsers must come back on a --cap-add-only patch.
func TestCurrentSecurityContext_KeepsHostUsers(t *testing.T) {
	sc, err := currentSecurityContext([]byte(`{"spec":{"securityContext":{"hostUsers":false,"capabilities":{"add":["SETUID"]}}}}`))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(sc)
	if string(b) != `{"capabilities":{"add":["SETUID"]},"hostUsers":false}` {
		t.Errorf("round-tripped securityContext = %s", b)
	}
	empty, err := currentSecurityContext([]byte(`{"spec":{}}`))
	if err != nil || empty == nil {
		t.Fatalf("absent block = %v, %v; want empty context", empty, err)
	}
}

func TestServiceSetCommands_RegisterUserNamespace(t *testing.T) {
	if serviceSetCmd.Flags().Lookup("user-namespace") == nil {
		t.Error("project service set lacks --user-namespace")
	}
	if serviceSetTopCmd.Flags().Lookup("user-namespace") == nil {
		t.Error("service set lacks --user-namespace")
	}
}
