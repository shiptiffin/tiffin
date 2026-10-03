#!/usr/bin/env bash
# Seeding helper (run by seed-box-apps.sh, or again on its own): a seed sends
# its jobs and mail within a second or two, which no real shop does. This
# spreads the demo project's finished jobs and captured mail back over the
# last hours, at uneven gaps, keeping their order. Throwaway dev boxes only.
#
#   TIFFIN_LIMA_INSTANCE=dev-ui e2e/seed-box-spread.sh [project]
set -euo pipefail
: "${TIFFIN_LIMA_INSTANCE:?set TIFFIN_LIMA_INSTANCE to the dev box Lima instance}"
[ "$TIFFIN_LIMA_INSTANCE" = tiffin ] && { echo "seed-box-spread: not on the owner's box" >&2; exit 1; }
PROJECT="${1:-shop}"

# Jobs (the queue's own Postgres database): the newest finished job stays
# where it is; older ones get times at random gaps (minutes apart, with a
# quarter of them in bursts seconds apart, the way orders and emails come).
# Times are set from the newest job down, so running it again re-deals them.
limactl shell "$TIFFIN_LIMA_INSTANCE" -- sudo -u postgres psql -q -h /var/run/postgresql -d tiffin_queue -v ON_ERROR_STOP=1 -v p="$PROJECT" <<'SQL'
CREATE TEMP TABLE shift AS
WITH j AS (
  SELECT id, enqueued_at, row_number() OVER (ORDER BY id DESC) AS k FROM tq_jobs
  WHERE project = :'p' AND kind = 'job' AND state IN ('completed', 'dead', 'cancelled') AND finished_at > now() - interval '2 days'
), g AS (
  SELECT id, enqueued_at, k, CASE WHEN k = 1 THEN 0 ELSE (20 + -ln(1 - random()) * 420) * CASE WHEN random() < 0.25 THEN 0.08 ELSE 1 END END AS gap FROM j
)
SELECT id, enqueued_at - (first_value(enqueued_at) OVER (ORDER BY k) - sum(gap) OVER (ORDER BY k) * interval '1 second') AS d FROM g;
UPDATE tq_jobs t SET enqueued_at = t.enqueued_at - s.d, run_at = t.run_at - s.d, started_at = t.started_at - s.d,
  finished_at = t.finished_at - s.d, lease_until = t.lease_until - s.d
FROM shift s WHERE t.id = s.id;
UPDATE tq_attempts a SET started_at = a.started_at - s.d, finished_at = a.finished_at - s.d FROM shift s WHERE a.job_id = s.id;
SQL

# Mail (the box's state database): oldest message furthest back, at uneven gaps.
limactl shell "$TIFFIN_LIMA_INSTANCE" -- sudo python3 - "$PROJECT" <<'PY'
import datetime as dt, sqlite3, sys
project = sys.argv[1]
db = sqlite3.connect("/var/lib/tiffin/platform/state.db", timeout=10)
gaps = [9, 38, 81, 47, 126, 63, 210, 95, 174]  # minutes between messages, newest first
now = dt.datetime.now(dt.timezone.utc)
iso = lambda t: t.strftime("%Y-%m-%dT%H:%M:%S.%f") + "000Z"
for p in (project, "notes"):
    ids = [r[0] for r in db.execute("SELECT id FROM email_messages WHERE project = ? ORDER BY id DESC", (p,))]
    t = now - dt.timedelta(minutes=4 if p == project else 312)
    with db:
        for i, mid in enumerate(ids):
            t -= dt.timedelta(minutes=gaps[i % len(gaps)], seconds=(i * 37) % 53)
            db.execute("UPDATE email_messages SET created_at = ?, sent_at = CASE sent_at WHEN '' THEN '' ELSE ? END WHERE id = ?", (iso(t), iso(t), mid))
print("seed-box-spread: jobs and mail spread over the last hours")
PY
