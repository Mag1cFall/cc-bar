package desktop

import _ "embed"

//go:embed assets/icon.png
var icon []byte

func appIcon() []byte {
	return icon
}
