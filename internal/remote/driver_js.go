//go:build js

package remote

import (
	_ "github.com/ncruces/go-sqlite3/driver"
	_ "github.com/ncruces/go-sqlite3/vfs/memdb"
)

const sqlDriver = "sqlite3"

// dataSource keeps a brain's database in memory, where a browser holds the brain it decrypts.
func dataSource(file string) string { return "file:/" + file + "?vfs=memdb&" }
