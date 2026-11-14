// Package web holds the browser front end: a single HTML page (with its CSS
// and JavaScript inside it) that talks to the same JSON API as curl does.
//
// go:embed copies index.html into the compiled program, so the API binary
// is all you need to run; there are no separate files to ship.
package web

import _ "embed"

//go:embed index.html
var Index []byte
