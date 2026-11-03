//go:build !noweb

package main

// The web dashboard is compiled in unless built with -tags noweb.
import _ "github.com/Grano22/aiculator/plugins/webdash"
