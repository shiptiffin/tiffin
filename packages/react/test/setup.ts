// happy-dom for components, but keep Bun's own fetch/Request/Response so the
// real auth engine (run in-process by the tests) behaves as on the box.
import { GlobalRegistrator } from "@happy-dom/global-registrator";

const keep = ["fetch", "Request", "Response", "Headers", "FormData", "Blob", "File", "URL", "URLSearchParams", "AbortController", "AbortSignal", "TextEncoder", "TextDecoder", "crypto", "ReadableStream", "WritableStream", "TransformStream"] as const;
const saved = Object.fromEntries(keep.map((k) => [k, (globalThis as any)[k]]));
GlobalRegistrator.register({ url: "https://shop.tiffin.localhost:8443/sign-in", width: 1280, height: 900 });
for (const k of keep) (globalThis as any)[k] = saved[k];
(globalThis as any).IS_REACT_ACT_ENVIRONMENT = true;
