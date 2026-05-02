package kusoCli

import (
	"math/rand"
	"os/exec"
	"time"
)

// checkBinary returns true if `binary` resolves on $PATH. Used by
// debug to verify required CLIs are present.
func checkBinary(binary string) bool {
	_, err := exec.LookPath(binary)
	return err == nil
}

// generateRandomString picks `length` runes from `chars` (or a default
// alphanumeric+symbol set when empty). Used to suggest tunnel
// subdomains.
func generateRandomString(length int, chars string) string {
	if chars == "" {
		chars = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789!+?._-%"
	}
	letters := []rune(chars)
	src := rand.New(rand.NewSource(time.Now().UnixNano()))
	out := make([]rune, length)
	for i := range out {
		out[i] = letters[src.Intn(len(letters))]
	}
	return string(out)
}
