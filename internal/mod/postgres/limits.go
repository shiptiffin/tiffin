package postgres

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5"
)

// Per-project limits. Every project's role runs with safety settings, limit
// or not: a query time limit, an idle-in-transaction limit, a cap on the
// temporary files one query may write and a connection limit. A project
// with a limit (budget.SharedLimit: N% of the box) gets N% of the
// connections, and the watcher (cgroups.go) holds its backends' CPU to N%
// of the box's CPUs.

const (
	// DefaultStatementTimeout is how long one query may run, in seconds,
	// unless the project's config says otherwise. A project with a limit gets
	// LimitedStatementTimeout instead: its queries share less of the box.
	DefaultStatementTimeout = 300
	LimitedStatementTimeout = 30
	// idleInTransaction closes a session that sits in an open transaction
	// this long (seconds): it holds locks and keeps vacuum from cleaning up.
	idleInTransaction = 60
	// openTempPercent is the share of the data disk one query's temporary
	// files may take for a project without a limit.
	openTempPercent = 25
	minTempFileMB   = 256
)

// RoleLimits are the settings a project's role runs with.
type RoleLimits struct {
	Connections              int
	StatementTimeoutSeconds  int
	IdleInTransactionSeconds int
	TempFileLimitMB          int64 // 0: none (the data disk could not be measured)
}

// roleLimits computes a project's role settings: share is its limit (N% of
// the box, 0 for none), timeout its config's statementTimeoutSeconds (0 for
// the default), maxConn the cluster's max_connections and disk the data
// disk's size in bytes (0 unknown).
func roleLimits(share, timeout, maxConn int, disk uint64) RoleLimits {
	// Without a limit a project may hold four fifths of the connections,
	// leaving the rest to the box and the other projects.
	open := maxConn * 4 / 5
	l := RoleLimits{Connections: open, StatementTimeoutSeconds: DefaultStatementTimeout, IdleInTransactionSeconds: idleInTransaction}
	pct := openTempPercent
	if share > 0 {
		l.Connections = min(open, max(3, maxConn*share/100))
		l.StatementTimeoutSeconds = LimitedStatementTimeout
		pct = share
	}
	if timeout > 0 {
		l.StatementTimeoutSeconds = timeout
	}
	if disk > 0 {
		l.TempFileLimitMB = max(minTempFileMB, int64(disk>>20)*int64(pct)/100)
	}
	return l
}

// readConnections caps a project's read role (its builds): no more than the
// project's own role may hold.
const readConnections = 20

// readLimits are the settings a project's read role runs with: the same
// safety settings as the project's role, and at most readConnections.
func readLimits(l RoleLimits) RoleLimits {
	l.Connections = min(l.Connections, readConnections)
	return l
}

// statements are the SQL that give role these settings. They apply to new
// sessions; open ones keep theirs (nothing is cut off when a limit changes).
func (l RoleLimits) statements(role string) []string {
	r := quoteIdent(role)
	out := []string{
		fmt.Sprintf(`ALTER ROLE %s CONNECTION LIMIT %d`, r, l.Connections),
		fmt.Sprintf(`ALTER ROLE %s SET statement_timeout = '%ds'`, r, l.StatementTimeoutSeconds),
		fmt.Sprintf(`ALTER ROLE %s SET idle_in_transaction_session_timeout = '%ds'`, r, l.IdleInTransactionSeconds),
	}
	if l.TempFileLimitMB > 0 {
		out = append(out, fmt.Sprintf(`ALTER ROLE %s SET temp_file_limit = '%dMB'`, r, l.TempFileLimitMB))
	} else {
		out = append(out, fmt.Sprintf(`ALTER ROLE %s RESET temp_file_limit`, r))
	}
	return out
}

// applyRoleLimits gives a project's role its settings.
func applyRoleLimits(ctx context.Context, admin *pgx.Conn, role string, l RoleLimits) error {
	for _, s := range l.statements(role) {
		if _, err := admin.Exec(ctx, s); err != nil {
			return fmt.Errorf("%s: %w", s, err)
		}
	}
	return nil
}

// dataDiskBytes is the size of the disk the cluster lives on (0 unknown).
func dataDiskBytes() uint64 {
	var st syscall.Statfs_t
	if err := syscall.Statfs(DataDir, &st); err != nil {
		return 0
	}
	return uint64(st.Blocks) * uint64(st.Bsize)
}

// ---- queries stopped by the time limit ----

const timeoutMessage = "canceling statement due to statement timeout"

// countTimeouts counts, per role, the log lines that say a query was
// stopped by its time limit. Lines look like (log_line_prefix in Config)
// "2026-10-04 10:00:00.123 UTC [1234] p_shop@p_shop ERROR:  canceling ...".
func countTimeouts(r io.Reader) map[string]int {
	out := map[string]int{}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		before, _, ok := strings.Cut(line, " ERROR:  "+timeoutMessage)
		if !ok {
			continue
		}
		f := strings.Fields(before)
		if len(f) == 0 {
			continue
		}
		if user, _, ok := strings.Cut(f[len(f)-1], "@"); ok && user != "" {
			out[user]++
		}
	}
	return out
}

// timeoutsSince reads Postgres's journal since t and counts the queries
// each role had stopped by its time limit.
func timeoutsSince(ctx context.Context, t time.Time) (map[string]int, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "journalctl", "-u", UnitName, "--since", t.UTC().Format("2006-01-02 15:04:05")+" UTC",
		"-o", "cat", "--no-pager", "-q", "-g", "statement timeout").Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) && ee.ExitCode() == 1 && len(out) == 0 {
			return map[string]int{}, nil // nothing matched
		}
		return nil, err
	}
	return countTimeouts(strings.NewReader(string(out))), nil
}
