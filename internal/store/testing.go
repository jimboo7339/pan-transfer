package store

import "database/sql"

// DB exposes the underlying *sql.DB for tests in other packages.
//
// Tests in httpapi need to seed rows (tasks, runs) directly through SQL because
// the service layer requires a live driver instance. Exporting a narrow accessor
// here is preferable to duplicating the whole schema in another package, or to
// making Store.db public and inviting production code to bypass the repositories.
func (s *Store) DB() *sql.DB { return s.db }

// Now returns the current timestamp in the same format as persisted rows.
// Tests need it to insert rows whose time columns match the scan format.
func Now() string { return nowStr() }
