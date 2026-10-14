package uploads

import "testing"

func TestClassifyPasteText(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"empty", "", "plain text"},
		{"plain", "Just a few words.\n", "plain text"},
		{"go", "package main\n\nimport \"fmt\"\n\nfunc main() {\n\tfmt.Println()\n}\n", "Go"},
		{"c", "#include <stdio.h>\nint main(void) { return 0; }\n", "C"},
		{"java", "public class X {\n  public static void main(String[] a) { }\n}\n", "Java"},
		{"python", "def f():\n    pass\n", "Python"},
		{"shebang py", "#!/usr/bin/env python3\nprint(1)\n", "Python"},
		{"html", "<!DOCTYPE html>\n<html><body>x</body></html>\n", "HTML"},
		{"json", `{"a":1,"b":true}` + "\n", "JSON"},
		{"prose", "This is a long piece of natural language prose meant to look like a paragraph without code. " +
			"We keep typing until we have enough tokens to trigger the early prose path and avoid any accidental pattern match.",
			"plain text"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ClassifyPasteText([]byte(tc.in))
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}
