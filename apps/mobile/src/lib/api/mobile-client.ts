import { fetch } from "expo/fetch";

import { createUploadApi, type UploadApi } from "./uploads";

export function configuredUploadApi(): UploadApi {
  return createUploadApi(
    process.env.EXPO_PUBLIC_API_URL ?? "",
    (url, request) => fetch(url, request),
  );
}
