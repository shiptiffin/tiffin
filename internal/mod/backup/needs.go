package backup

// Needs: pgBackRest's repo belongs to the postgres user and its stanza
// targets the cluster, so Postgres (and Valkey, whose RDB it copies) install first.
func (*Module) Needs() []string { return []string{"postgres", "valkey"} }
