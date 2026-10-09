package email

import (
	"context"
	"os"
	"path/filepath"

	"github.com/shiptiffin/tiffin/internal/platform"
)

// ProjectDeleted runs once a destroyed project has no resources left. A
// destroy is final, so the project leaves no email behind: its message log
// (dev inbox and relayed mail alike) and their raw files, the delivery
// events and the rows that match them to messages, its suppression list,
// its rate limit and its sending-domain setup. A new project under the same
// name starts with an empty inbox. (The SMTP password and sender go when
// the email service is removed; see Reconcile.)
func (m *Module) ProjectDeleted(ctx context.Context, p *platform.Platform, project string) error {
	if project == "" || project == boxProject {
		return nil
	}
	db := p.DB.SQL()
	if err := ensureSchema(ctx, db); err != nil {
		return err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, q := range []string{
		`DELETE FROM email_events WHERE project = ?1 OR message IN (SELECT id FROM email_messages WHERE project = ?1)`,
		`DELETE FROM email_tracking WHERE project = ?1 OR id IN (SELECT id FROM email_messages WHERE project = ?1)`,
		`DELETE FROM email_messages WHERE project = ?1`,
		`DELETE FROM email_suppressions WHERE project = ?1`,
	} {
		if _, err := tx.ExecContext(ctx, q, project); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	if err := os.RemoveAll(filepath.Join(messagesDir(p.DataRoot), project)); err != nil {
		return err
	}
	m.sendMu.Lock()
	defer m.sendMu.Unlock()
	for _, k := range []string{"rate/" + project, sendingKey(project), "project/" + project} {
		if err := p.DB.KVDelete(ctx, kvNS, k); err != nil {
			return err
		}
	}
	return nil
}

var _ platform.ProjectCleaner = (*Module)(nil)
