// Package assets embeds the static dashboard so the runner can serve the UI
// from a single binary.
package assets

import "embed"

// WebFS contains the dashboard files under the web/ directory.
//
//go:embed web
var WebFS embed.FS
