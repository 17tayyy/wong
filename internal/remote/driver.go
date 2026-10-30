//go:build !js

package remote

import _ "modernc.org/sqlite"

const sqlDriver = "sqlite"

// dataSource opens a brain's database file in WAL mode.
func dataSource(file string) string { return "file:" + file + "?_pragma=journal_mode(WAL)&" }
