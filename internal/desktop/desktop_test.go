package desktop

import "testing"

// TestBackgroundArgument 仅显式后台参数允许隐藏启动
func TestBackgroundArgument(t *testing.T) {
	for _, test := range []struct {
		arguments []string
		want      bool
	}{{nil, false}, {[]string{"CCBar.exe"}, false}, {[]string{"--background"}, true}, {[]string{"CCBar.exe", "--background"}, true}} {
		if got := hasBackgroundArgument(test.arguments); got != test.want {
			t.Fatalf("arguments=%v background=%t want=%t", test.arguments, got, test.want)
		}
	}
}
