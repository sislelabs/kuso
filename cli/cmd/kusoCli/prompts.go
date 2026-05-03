package kusoCli

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"github.com/AlecAivazis/survey/v2"
)

// Interactive prompt helpers used by `kuso login` and `kuso remote`.
// Kept tiny on purpose — survey covers the heavy lifting.

// promptLine reads one line of input with a prompt + optional default.
// The --force flag short-circuits the read and accepts the default,
// which lets scripts run end-to-end without piping `echo`s in.
func promptLine(question, hint, def string) string {
	prefix := "? " + question
	if hint != "" {
		prefix += " " + hint
	}
	if def != "" {
		prefix += " [" + def + "]"
	}
	prefix += ": "

	if def != "" && force {
		fmt.Println(prefix + def)
		return def
	}

	fmt.Print(prefix)
	reader := bufio.NewReader(os.Stdin)
	text, _ := reader.ReadString('\n')
	text = strings.TrimSpace(text)
	if text == "" {
		text = def
	}
	return text
}

// selectFromList shows a survey-style picker. Falls back to the
// default in --force mode so scripts don't deadlock waiting for a
// keystroke that will never come.
func selectFromList(question string, options []string, def string) string {
	if def != "" && force {
		return def
	}
	answer := def
	prompt := &survey.Select{
		Message: question,
		Options: options,
		Default: def,
	}
	if err := survey.AskOne(prompt, &answer); err != nil {
		fmt.Fprintln(os.Stderr, "kuso:", err)
		return ""
	}
	return answer
}
