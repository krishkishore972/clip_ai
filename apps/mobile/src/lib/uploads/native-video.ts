import * as DocumentPicker from "expo-document-picker";
import { File, FileMode } from "expo-file-system";
import { Platform } from "react-native";

import type { CreateUploadRequest } from "../api/generated";
import { type ChunkSource, videoMetadata } from "./upload-video";

export interface SelectedVideo {
  metadata: CreateUploadRequest;
  open(): ChunkSource;
}

// The picker reports the original name/type/size; the File API reads whichever
// URI it returned. Both are included in failures so a device report says which
// file was rejected and why.
function describe(asset: DocumentPicker.DocumentPickerAsset, file: File) {
  let cached: string;
  try {
    cached = `exists=${file.exists}, size=${file.size}`;
  } catch (error) {
    cached = `unreadable (${error instanceof Error ? error.message : "unknown error"})`;
  }
  return `"${asset.name}" (${asset.mimeType ?? "unknown type"}, ${
    asset.size ?? "unknown"
  } bytes picked, file ${cached})`;
}

// `File.exists`/`File.size` can disagree with what the native reader allows, so
// take the size from the same handle the upload reads from.
function readableSize(file: File): number {
  const handle = file.open(FileMode.ReadOnly);
  try {
    const size = handle.size;
    if (!size) throw new Error("the selected file is empty");
    return size;
  } finally {
    handle.close();
  }
}

export async function pickVideo(): Promise<SelectedVideo | null> {
  const result = await DocumentPicker.getDocumentAsync({
    type: "video/*",
    multiple: false,
    // The File API is denied READ on the picker's cache copy on Android
    // ("Missing 'READ' permission"), so read the original content:// URI
    // instead, which the temporary picker grant makes readable. iOS keeps the
    // documented cache-copy behaviour.
    copyToCacheDirectory: Platform.OS === "ios",
  });
  if (result.canceled) return null;
  const asset = result.assets[0];
  if (!asset) throw new Error("The picker returned no video.");
  const file = new File(asset.uri);
  let sizeBytes: number;
  try {
    sizeBytes = readableSize(file);
  } catch (error) {
    console.warn("Selected video could not be opened", asset.uri, error);
    throw new Error(
      `${describe(asset, file)} could not be opened: ${
        error instanceof Error ? error.message : "unknown error"
      }`,
    );
  }
  let metadata: CreateUploadRequest;
  try {
    metadata = videoMetadata(asset.name, sizeBytes);
  } catch (error) {
    throw new Error(
      `${describe(asset, file)} was rejected: ${
        error instanceof Error ? error.message : "unknown error"
      }`,
    );
  }
  return {
    metadata,
    open() {
      const handle = file.open(FileMode.ReadOnly);
      return {
        sizeBytes: metadata.sizeBytes,
        async read(offset, length) {
          // Reads come from this open handle, so they cannot drift from the
          // reported size. Re-reading File.exists/size/lastModified per part is
          // not reliable on Android and would abort a healthy upload.
          if (handle.size !== metadata.sizeBytes) {
            throw new Error(
              "The selected video changed or is no longer accessible.",
            );
          }
          handle.offset = offset;
          const first = await handle.readBytes(length);
          if (first.byteLength === length) return first;
          // A read may be short. Still bound memory by one part, never the file.
          const bytes = new Uint8Array(length);
          bytes.set(first);
          let filled = first.byteLength;
          while (filled < length) {
            const next = await handle.readBytes(length - filled);
            if (next.byteLength === 0)
              throw new Error(
                "The video ended before the expected part boundary.",
              );
            bytes.set(next, filled);
            filled += next.byteLength;
          }
          return bytes;
        },
        close() {
          handle.close();
        },
      };
    },
  };
}
