// Package testfiles embeds the fixtures made by the xz command line tool
// (see internal/mkfixtures). They are embedded rather than read from
// testdata/ because the emulated CI lanes run cross-compiled test binaries in
// a container that has no source tree.
package testfiles

import (
	"embed"
	"io/fs"
)

//go:embed data
var data embed.FS

// FS holds the fixture files and the INDEX that describes them.
var FS, _ = fs.Sub(data, "data")
