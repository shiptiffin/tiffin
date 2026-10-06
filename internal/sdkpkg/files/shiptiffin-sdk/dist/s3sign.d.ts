export interface S3Creds {
    accessKeyId: string;
    secretAccessKey: string;
    region: string;
}
/** URI-encodes every byte but A-Z a-z 0-9 - . _ ~ (and "/" when keepSlash). */
export declare function s3Escape(s: string, keepSlash?: boolean): string;
/** The path-style path of a bucket and key. */
export declare function objectPath(bucket: string, key: string): string;
export interface PresignInput {
    method: string;
    /** scheme://host[:port] */
    endpoint: string;
    bucket: string;
    key: string;
    creds: S3Creds;
    /** Seconds, 1-604800. */
    expiresIn: number;
    /** Extra query parameters (uploadId, partNumber, x-tiffin-max-size): signed. */
    query?: Record<string, string>;
    /** Headers the request must send with exactly these values (content-type, content-length): signed. */
    headers?: Record<string, string>;
    now?: Date;
}
/** A presigned URL, the same as the box's own (internal/mod/storage PresignWith). */
export declare function presignUrl(i: PresignInput): string;
export interface SignedRequestInput {
    method: string;
    endpoint: string;
    bucket: string;
    key: string;
    creds: S3Creds;
    query?: Record<string, string>;
    headers?: Record<string, string>;
    now?: Date;
}
/** The URL and headers (Authorization included) of a header-signed request with an unsigned payload. */
export declare function signRequest(i: SignedRequestInput): {
    url: string;
    headers: Record<string, string>;
};
