//go:build !dev

package webspa

import "embed"

//go:embed all:out
var SPA embed.FS
