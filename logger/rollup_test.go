package logger

import (
	"strings"
	"testing"
)

func TestParseDayLog(t *testing.T) {
	in := `[14:01:02] <alice> !weather Madrid
[14:02:00] <bob> hello
[14:03:00] * bob waves
[14:04:00] <Bot> !spam
[15:00:00] *** alice sets mode +ov bob carol
[15:01:00] *** alice sets mode -o bob
[15:02:00] *** alice sets mode +k secret
[15:03:00] *** carol changed the topic to: new topic
[15:04:00] *** dave has joined #c
[15:05:00] *** dave has left #c (bye)
`
	d := ParseDayLog(strings.NewReader(in), "bot", "!")
	if d.Msgs != 2 || d.Actions != 1 || d.Hours[14] != 3 || d.Cmds["weather"] != 1 || d.Cmds["spam"] != 0 {
		t.Fatalf("msgs/cmds: %+v", d)
	}
	if d.Modes["+o"] != 1 || d.Modes["+v"] != 1 || d.Modes["-o"] != 1 || len(d.Modes) != 3 || d.ModeBy["alice"] != 3 {
		t.Fatalf("modes: %+v", d.Modes)
	}
	if d.Topics != 1 || d.TopicLog[0].Nick != "carol" || d.TopicLog[0].Time != "15:03" || d.Joins != 1 || d.Parts != 1 {
		t.Fatalf("topic/joinpart: %+v", d)
	}
}
