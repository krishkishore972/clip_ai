// Generated from apps/api/openapi/openapi.yaml. Do not edit.
// Regenerate: pnpm --filter mobile gen:api

export type Me = {
  "userId": string;
  "workspaceId": string;
};

export type Token = string;

export type PartNumber = number;

export type CreateUploadRequest = {
  "filename": string;
  "sizeBytes": number;
  "contentType": "video/mp4" | "video/quicktime" | "video/webm";
};

export type SignUploadPartsRequest = {
  "partNumbers": (PartNumber)[];
};

export type CompleteUploadPart = {
  "partNumber": PartNumber;
  "eTag": string;
};

export type CompleteUploadRequest = {
  "parts": (CompleteUploadPart)[];
};

export type UploadedPart = {
  "partNumber": PartNumber;
  "eTag": string;
  "sizeBytes": number;
};

export type UploadSession = {
  "uploadId": Token;
  "uploadToken": string;
  "status": "uploading" | "uploaded_unvalidated";
  "partSizeBytes": number;
  "partCount": number;
  "expiresAt": string;
  "uploadedParts": (UploadedPart)[] | null;
};

export type SignedUploadPart = {
  "partNumber": PartNumber;
  "url": string;
  "headers": Record<string, string> | null;
  "expiresAt": string;
};

export type SignedUploadParts = {
  "parts": (SignedUploadPart)[];
};

export type APIError = {
  "code": string;
  "message": string;
  "details"?: unknown;
  "traceId"?: string;
};
