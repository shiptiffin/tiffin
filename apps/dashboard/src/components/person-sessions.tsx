import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import type { Person } from "@/api/client";
import { sessions, sq } from "@/api/sessions";
import { Skeleton } from "@/components/page";
import { ProblemNote } from "@/components/problem";
import { SessionRows } from "@/components/sessions-list";
import { toast } from "@/components/toast";
import { Button } from "@/components/ui/button";
import { Dialog, DialogBody, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { countWords } from "@/lib/format";

/**
 * People › End sessions (owners and admins): where someone is signed in now,
 * with Sign out on each and Sign out everywhere. Their access stays; they can
 * sign in again. To take access away, remove them instead.
 */
export function PersonSessionsDialog({ person, onClose }: { person: Person | null; onClose: () => void }) {
  return (
    <Dialog open={!!person} onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="sm:max-w-[40rem]">{person && <PersonSessions key={person.id} person={person} onClose={onClose} />}</DialogContent>
    </Dialog>
  );
}

function PersonSessions({ person, onClose }: { person: Person; onClose: () => void }) {
  const qc = useQueryClient();
  const all = useQuery(sq.sessions(person.id));
  const open = (all.data ?? []).filter((s) => s.state === "active");
  const first = person.name.split(" ")[0];
  const end = useMutation({
    mutationFn: () => sessions.endOthers(person.id),
    onSuccess: (r) => {
      void qc.invalidateQueries({ queryKey: ["sessions"] });
      toast({ title: `Signed ${first} out of ${countWords(r.ended, "session")}.` });
      onClose();
    },
  });
  return (
    <>
      <DialogHeader>
        <DialogTitle>{`${first}’s sessions`}</DialogTitle>
        <DialogDescription>
          Where {first} is signed in now. Signing out ends a session at once; API keys {first} made keep working until revoked. {first} can still sign in again; to take access
          away, remove them from the box.
        </DialogDescription>
      </DialogHeader>
      <DialogBody>
        {all.isPending && <Skeleton className="h-24" />}
        {all.isError && <ProblemNote error={all.error} />}
        {all.isSuccess && open.length === 0 && <p className="border-y border-rule py-4 text-[0.875rem] text-ink-3">{first} isn’t signed in anywhere.</p>}
        {open.length > 0 && <SessionRows list={open} />}
        {end.isError && <ProblemNote className="mt-4" error={end.error} />}
      </DialogBody>
      <DialogFooter>
        <Button variant="ghost" onClick={onClose}>
          Close
        </Button>
        {open.length > 0 && (
          <Button variant="primary" disabled={end.isPending} onClick={() => end.mutate()}>
            {end.isPending ? "Signing out…" : "Sign out everywhere"}
          </Button>
        )}
      </DialogFooter>
    </>
  );
}
