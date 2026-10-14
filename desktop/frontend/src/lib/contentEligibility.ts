import type { Transcription } from "./types";

/** Accepted uploads retain their source audio throughout the job lifecycle. */
export function hasStoredAudio(transcription: Transcription): boolean {
  return transcription.audio.sizeBytes > 0 && Boolean(transcription.audio.filename.trim());
}

export function hasDownloadableTranscript(transcription: Transcription): boolean {
  return transcription.status === "succeeded" && transcription.result != null;
}
