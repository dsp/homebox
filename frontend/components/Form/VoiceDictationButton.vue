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
  import { useVoiceDictation } from "~~/composables/use-voice-dictation";
  import { usePublicApi } from "~~/composables/use-api";
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

  // Shared key so any number of dictation buttons resolve one status call.
  const { data: status } = useAsyncData("api-status-speech", async () => {
    const { data, error } = await pubApi.status();
    return error ? null : data;
  });

  const { isSupported, isRecording, isTranscribing, toggle } = useVoiceDictation({
    onText: text => emit("text", text),
    onError: kind => {
      if (kind === "permission") {
        toast.error(t("components.form.voice.toast.permission_denied"));
      } else {
        toast.error(t("components.form.voice.toast.transcribe_failed"));
      }
    },
  });

  // Render nothing unless the server can transcribe and the browser can
  // record — an unusable mic button is worse than none.
  const available = computed(() => (status.value?.speechToText ?? false) && isSupported.value);

  const tooltip = computed(() =>
    isRecording.value ? t("components.form.voice.stop_tooltip") : t("components.form.voice.start_tooltip")
  );
</script>
