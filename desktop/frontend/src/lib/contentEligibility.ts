import type { Transcription } from "./types";

/** Accepted uploads retain their source audio throughout the job lifecycle. */
export function hasStoredAudio(transcription: Transcription): boolean {
  return transcription.audio.sizeBytes > 0 && Boolean(transcription.audio.filename.trim());
}

export function hasDownloadableTranscript(transcription: Transcription): boolean {
  return transcription.status === "succeeded" && transcription.result != null;
}

/** The Library lists only finished items: a successful result and stored audio. The server applies the same rule. */
export function isLibraryEligible(transcription: Transcription): boolean {
  return hasStoredAudio(transcription) && hasDownloadableTranscript(transcription);
}
