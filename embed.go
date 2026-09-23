// Package partsviz holds the embedded web frontend.
package partsviz

import "embed"

//go:embed web
var Web embed.FS
