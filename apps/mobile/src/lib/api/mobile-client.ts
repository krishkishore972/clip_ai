import { fetch } from "expo/fetch";

import { createUploadApi, type UploadApi } from "./uploads";
import { createPreparationApi } from "./preparation";

export function configuredPreparationApi(
  getToken: () => Promise<string | null>,
) {
  return createPreparationApi(
    process.env.EXPO_PUBLIC_API_URL ?? "",
    (url, request) => fetch(url, request),
    getToken,
  );
}

export function configuredUploadApi(
  getToken: () => Promise<string | null>,
): UploadApi {
  return createUploadApi(
    process.env.EXPO_PUBLIC_API_URL ?? "",
    (url, request) => fetch(url, request),
    getToken,
  );
}
