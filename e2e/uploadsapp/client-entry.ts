// Bundled to public/client.js for the browser: the test page calls
// window.tiffin.uploadFile.
import { uploadFile, UploadError } from "../../packages/sdk/src/storage-client";

(globalThis as unknown as { tiffin: unknown }).tiffin = { uploadFile, UploadError };
