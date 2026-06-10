// Standalone module for the Discord incident bridge bot. Kept separate from
// server-go so the discordgo dependency never enters the control-plane build.
// Not part of the root go.work by default — see the go.work note in the
// component README / deploy comments. Build it on its own from this dir.
module kuso/incident-bot

go 1.26.0

require github.com/bwmarrin/discordgo v0.29.0

require (
	github.com/gorilla/websocket v1.5.3 // indirect
	golang.org/x/crypto v0.31.0 // indirect
	golang.org/x/sys v0.28.0 // indirect
)
