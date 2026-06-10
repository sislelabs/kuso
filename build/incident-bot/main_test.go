package main

import "testing"

func TestMessageToAction(t *testing.T) {
	tests := []struct {
		name     string
		content  string
		reaction string
		want     string
	}{
		// reactions take precedence and ignore content
		{"react go", "", emojiGo, "go"},
		{"react reject", "", emojiReject, "reject"},
		{"react unrelated", "", "🎉", ""},
		{"react go ignores content", "reject", emojiGo, "go"},

		// keyword content -> go
		{"go lower", "go", "", "go"},
		{"go upper", "GO", "", "go"},
		{"go padded", "  go  ", "", "go"},
		{"approve", "approve", "", "go"},
		{"ship", "ship", "", "go"},
		{"yes", "yes", "", "go"},
		{"lgtm", "LGTM", "", "go"},

		// keyword content -> reject
		{"reject", "reject", "", "reject"},
		{"no", "no", "", "reject"},
		{"cancel", "Cancel", "", "reject"},
		{"stop", "stop", "", "reject"},
		{"abort", "abort", "", "reject"},

		// free text -> text
		{"freeform", "actually it's the migration lock", "", "text"},
		{"go in sentence is text", "go check the migration", "", "text"},
		{"reject in sentence is text", "do not reject the connection", "", "text"},

		// nothing to do
		{"empty both", "", "", ""},
		{"whitespace only", "   ", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := messageToAction(tt.content, tt.reaction); got != tt.want {
				t.Errorf("messageToAction(%q, %q) = %q, want %q", tt.content, tt.reaction, got, tt.want)
			}
		})
	}
}

func TestThreadName(t *testing.T) {
	tests := []struct {
		name string
		in   incident
		want string
	}{
		{
			name: "project and service",
			in:   incident{ID: "inc-1", Project: "shop", Service: "api", Title: "CrashLoopBackOff"},
			want: "incident: shop/api — CrashLoopBackOff",
		},
		{
			name: "no service",
			in:   incident{ID: "inc-2", Project: "shop", Title: "node down"},
			want: "incident: shop — node down",
		},
		{
			name: "falls back to id when no title",
			in:   incident{ID: "inc-3", Project: "shop"},
			want: "incident: shop — inc-3",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := threadName(tt.in); got != tt.want {
				t.Errorf("threadName = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestThreadNameTruncatedTo100(t *testing.T) {
	long := ""
	for i := 0; i < 200; i++ {
		long += "x"
	}
	got := threadName(incident{ID: "inc-x", Project: "p", Title: long})
	if len([]rune(got)) > 100 {
		t.Errorf("threadName length = %d runes, want <= 100", len([]rune(got)))
	}
}

func TestSplitForDiscord(t *testing.T) {
	// short passes through unchanged
	if got := splitForDiscord("hello"); len(got) != 1 || got[0] != "hello" {
		t.Fatalf("short split = %v, want [hello]", got)
	}

	// long is chunked, every chunk under the 2000 hard cap, and reassembles
	big := make([]byte, 5000)
	for i := range big {
		if i%80 == 0 {
			big[i] = '\n'
		} else {
			big[i] = 'a'
		}
	}
	chunks := splitForDiscord(string(big))
	if len(chunks) < 2 {
		t.Fatalf("expected multiple chunks, got %d", len(chunks))
	}
	var reassembled string
	for _, c := range chunks {
		if len(c) > 2000 {
			t.Errorf("chunk len %d exceeds Discord 2000 cap", len(c))
		}
		reassembled += c
	}
	if reassembled != string(big) {
		t.Errorf("reassembled chunks != original")
	}
}
