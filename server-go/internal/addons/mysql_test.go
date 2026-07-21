package addons

import "testing"

func TestMysqlAddonRejectsHA(t *testing.T) {
	if !noHAKinds["mysql"] {
		t.Fatal("mysql must be in noHAKinds (no HA template exists)")
	}
}
