// Package shared holds the popup page used by both stage 0c tray prototypes.
// Tauri reads this directory as its frontendDist; Wails embeds it from here.
package shared

import "embed"

//go:embed popup.html popup.js fonts icons/tray-32.png icons/tray-template-44.png
var FS embed.FS
