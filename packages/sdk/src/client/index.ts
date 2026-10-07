/**
 * `@shiptiffin/sdk/client`: browser helpers with no framework. Import what
 * you use; bundlers leave the rest out.
 *
 * - uploadFile: files straight to the box's storage (progress, parts, pause, resume)
 * - subscribeRun: live progress of a background job or workflow run
 * - prepareCaptcha / attachCaptcha: the box's bot check for sign-up and sign-in
 * - authConfig / errorText: which sign-in methods the app has, and errors in words
 *
 * Sign-in, sign-up and sessions use Better Auth's own client against the
 * box's /api/auth (see docs/guide/auth.md).
 */
export { UploadError, abortUpload, uploadFile, type UploadFileOptions, type UploadProgress, type UploadResult, type UploadTicket } from "./upload";
export { subscribeRun, type LiveRun, type RunSnapshot, type RunStep, type SubscribeOptions } from "./run";
export { attachCaptcha, prepareCaptcha, solveCaptcha } from "./captcha";
export { authConfig, errorText, type AuthConfig, type AuthOptions, type SocialProvider } from "./auth";
