package kusoCli

import "testing"

func TestApplyJQ_Field(t *testing.T) {
	out, err := applyJQ([]byte(`{"name":"web","port":3000}`), ".name")
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != "\"web\"\n" {
		t.Fatalf("jq .name = %q", out)
	}
}

func TestApplyJQ_Iterate(t *testing.T) {
	out, err := applyJQ([]byte(`{"items":[{"n":"a"},{"n":"b"}]}`), ".items[].n")
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != "\"a\"\n\"b\"\n" {
		t.Fatalf("jq iterate = %q", out)
	}
}

func TestApplyJQ_NotJSON(t *testing.T) {
	if _, err := applyJQ([]byte("not json"), ".x"); err == nil {
		t.Error("expected error for non-JSON body")
	}
}
