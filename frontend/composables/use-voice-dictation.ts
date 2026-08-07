import { useUserApi } from "./use-api";

export interface VoiceDictationOptions {
  /** Called with the transcribed text after a successful capture. */
  onText: (text: string) => void;
  /** Called when recording or transcription fails. */
  onError?: (kind: "unsupported" | "permission" | "transcribe") => void;
}

// Preference order matters: Chromium and Firefox record Opus-in-WebM, while
// iOS Safari only offers AAC-in-MP4. The backend forwards the content type to
// the transcription provider, which accepts both.
const mimeCandidates = ["audio/webm;codecs=opus", "audio/webm", "audio/mp4"];

// Hard stop so a forgotten open mic cannot record (and upload) indefinitely.
const maxRecordingMs = 60_000;

function pickMimeType(): string | undefined {
  return mimeCandidates.find(m => MediaRecorder.isTypeSupported(m));
}

function filenameFor(mimeType: string): string {
  return mimeType.includes("mp4") ? "clip.mp4" : "clip.webm";
}

/**
 * Push-to-talk voice dictation: records from the microphone with
 * MediaRecorder and sends the clip to the server-side transcription proxy.
 * Whether the server has transcription configured is a separate concern —
 * check `status.speechToText` before showing any UI that calls this.
 */
export function useVoiceDictation(options: VoiceDictationOptions) {
  const api = useUserApi();

  const isSupported = computed(
    () =>
      import.meta.client &&
      typeof MediaRecorder !== "undefined" &&
      !!navigator.mediaDevices &&
      typeof navigator.mediaDevices.getUserMedia === "function"
  );
  const isRecording = ref(false);
  const isTranscribing = ref(false);

  let recorder: MediaRecorder | null = null;
  let stream: MediaStream | null = null;
  let chunks: Blob[] = [];
  let stopTimer: ReturnType<typeof setTimeout> | null = null;

  function releaseStream() {
    stream?.getTracks().forEach(track => track.stop());
    stream = null;
    recorder = null;
    if (stopTimer) {
      clearTimeout(stopTimer);
      stopTimer = null;
    }
  }

  async function start() {
    if (isRecording.value || isTranscribing.value) {
      return;
    }
    if (!isSupported.value) {
      options.onError?.("unsupported");
      return;
    }

    try {
      stream = await navigator.mediaDevices.getUserMedia({ audio: true });
    } catch {
      options.onError?.("permission");
      return;
    }

    const mimeType = pickMimeType();
    recorder = mimeType ? new MediaRecorder(stream, { mimeType }) : new MediaRecorder(stream);
    chunks = [];

    recorder.ondataavailable = event => {
      if (event.data.size > 0) {
        chunks.push(event.data);
      }
    };
    recorder.onstop = () => {
      const type = recorder?.mimeType || mimeType || "audio/webm";
      releaseStream();
      isRecording.value = false;
      void transcribe(new Blob(chunks, { type }));
    };

    recorder.start();
    isRecording.value = true;
    stopTimer = setTimeout(stop, maxRecordingMs);
  }

  function stop() {
    if (recorder && recorder.state !== "inactive") {
      recorder.stop();
    }
  }

  async function transcribe(clip: Blob) {
    if (clip.size === 0) {
      return;
    }

    isTranscribing.value = true;
    try {
      const { data, error } = await api.actions.transcribe(clip, filenameFor(clip.type));
      if (error || !data.text) {
        options.onError?.("transcribe");
        return;
      }
      options.onText(data.text);
    } catch {
      options.onError?.("transcribe");
    } finally {
      isTranscribing.value = false;
    }
  }

  function toggle() {
    if (isRecording.value) {
      stop();
    } else {
      void start();
    }
  }

  onBeforeUnmount(() => {
    if (recorder && recorder.state !== "inactive") {
      // Drop the capture instead of transcribing on teardown.
      recorder.onstop = null;
      recorder.stop();
    }
    releaseStream();
  });

  return {
    isSupported,
    isRecording,
    isTranscribing,
    start,
    stop,
    toggle,
  };
}
