// Package persist exposes SQLite setup to the client integration fixtures.
package persist

import "uniclog.io/sonoryx/apps/server/internal/persist"

type Store = persist.Store

var Open = persist.Open
