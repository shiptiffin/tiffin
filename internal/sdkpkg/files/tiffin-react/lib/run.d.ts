export interface RunStep {
    name: string;
    kind: "step" | "sleep" | "event" | "approval" | "webhook";
    state: "completed" | "failed" | "waiting" | "timed_out" | "cancelled";
    startedAt: string;
    finishedAt?: string;
    waitUntil?: string;
}
/** The job or run as the box reports it. */
export interface RunSnapshot<P = unknown, O = unknown> {
    id: string;
    type: "job" | "run";
    /** Queue or workflow name. */
    name: string;
    /** Jobs: scheduled, queued, running, retrying, completed, dead, cancelled. Runs: running, waiting, completed, failed, cancelled. */
    status: string;
    done: boolean;
    progress: P | null;
    output: O | null;
    error?: string;
    attempt?: number;
    waitingFor?: string;
    steps?: RunStep[];
}
export interface LiveRun<P = unknown, O = unknown, C = unknown> {
    /** null until the first state arrives. */
    status: string | null;
    /** The latest value the job or run reported (job.progress, ctx.progress). */
    progress: P | null;
    /** The result, once it completed. */
    output: O | null;
    /** Why it failed, or why the browser cannot watch it (a bad or expired token). */
    error: string | null;
    /** Output chunks (job.log, ctx.stream), in order. */
    chunks: C[];
    done: boolean;
    /** True while the stream is open (it reconnects by itself). */
    connected: boolean;
    run: RunSnapshot<P, O> | null;
}
export interface SubscribeOptions {
    /** Origin of the app; default this page's. */
    baseUrl?: string;
    fetch?: typeof fetch;
}
/**
 * Watches a job or run; onChange gets every new state. Reconnects with
 * Last-Event-ID (backing off up to 15 s) until the job or run finishes or the
 * returned function is called.
 */
export declare function subscribeRun<P = unknown, O = unknown, C = unknown>(id: string, token: string, onChange: (s: LiveRun<P, O, C>) => void, opts?: SubscribeOptions): () => void;
/**
 * Live state of a job or workflow run: status, progress, output, error and
 * output chunks. Pass the id and token a server action returned; null or
 * undefined waits.
 */
export declare function useRun<P = unknown, O = unknown, C = unknown>(id: string | null | undefined, token: string | null | undefined, opts?: SubscribeOptions): LiveRun<P, O, C>;
/** useRun for a queue job (job_...). */
export declare const useJob: typeof useRun;
