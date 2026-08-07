<template>
  <TooltipProvider v-if="available" :delay-duration="0">
    <Tooltip>
      <TooltipTrigger as-child>
        <Button
          type="button"
          :variant="variant"
          size="icon"
          :disabled="disabled || isTranscribing"
          :aria-label="tooltip"
          :aria-pressed="isRecording"
          v-bind="$attrs"
          @click="toggle"
        >
          <MdiLoading v-if="isTranscribing" class="size-5 animate-spin" />
          <MdiStop v-else-if="isRecording" class="size-5 animate-pulse text-destructive" />
          <MdiMicrophone v-else class="size-5" />
        </Button>
      </TooltipTrigger>
      <TooltipContent>
        <p>{{ tooltip }}</p>
      </TooltipContent>
    </Tooltip>
  </TooltipProvider>
</template>

<script setup lang="ts">
  import { useI18n } from "vue-i18n";
  import { toast } from "@/components/ui/sonner";
  import { Button } from "~/components/ui/button";
  import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "~/components/ui/tooltip";
  import { useVoiceDictation, type VoiceDictationErrorKind } from "~~/composables/use-voice-dictation";
  import { usePublicApi } from "~~/composables/use-api";
  import type { APISummary } from "~~/lib/api/types/data-contracts";
  import MdiMicrophone from "~icons/mdi/microphone";
  import MdiStop from "~icons/mdi/stop";
  import MdiLoading from "~icons/mdi/loading";

  defineOptions({
    inheritAttrs: false,
  });

  withDefaults(
    defineProps<{
      disabled?: boolean;
      variant?: "outline" | "ghost";
    }>(),
    {
      disabled: false,
      variant: "outline",
    }
  );

  const emit = defineEmits<{
    text: [value: string];
  }>();

  const { t } = useI18n();
  const pubApi = usePublicApi();

  // App-wide cache: useState survives component unmounts (useAsyncData purges
  // its cache when the last subscriber unmounts, which would refetch status
  // on every modal open). Server config rarely changes within a session.
  const status = useState<APISummary | null>("speech-status", () => null);
  const statusRequested = useState("speech-status-requested", () => false);
  if (!statusRequested.value) {
    statusRequested.value = true;
    pubApi.status().then(({ data, error }) => {
      if (!error) {
        status.value = data;
      }
    });
  }

  const errorToasts: Record<VoiceDictationErrorKind, string> = {
    permission: "components.form.voice.toast.permission_denied",
    unavailable: "components.form.voice.toast.mic_unavailable",
    unsupported: "components.form.voice.toast.mic_unavailable",
    empty: "components.form.voice.toast.empty_recording",
    transcribe: "components.form.voice.toast.transcribe_failed",
  };

  const { isSupported, isRecording, isTranscribing, toggle } = useVoiceDictation({
    onText: text => emit("text", text),
    onError: kind => toast.error(t(errorToasts[kind])),
  });

  // Render nothing unless the server can transcribe and the browser can
  // record — an unusable mic button is worse than none.
  const available = computed(() => (status.value?.speechToText ?? false) && isSupported.value);

  const tooltip = computed(() =>
    isRecording.value ? t("components.form.voice.stop_tooltip") : t("components.form.voice.start_tooltip")
  );
</script>
