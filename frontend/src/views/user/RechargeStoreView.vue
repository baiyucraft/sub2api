<template>
  <AppLayout>
    <div class="recharge-store-layout">
      <div class="recharge-store-toolbar">
        <div class="flex min-w-0 items-start gap-3">
          <div class="store-mark" aria-hidden="true">
            <Icon name="infoCircle" size="sm" />
          </div>
          <p class="min-w-0 text-sm leading-5 text-gray-600 dark:text-dark-300">
            <span class="hidden sm:inline">{{ t('rechargeStore.embedHint') }}</span>
            <span class="sm:hidden">{{ t('rechargeStore.mobileHint') }}</span>
          </p>
        </div>

        <a
          :href="RECHARGE_STORE_URL"
          target="_blank"
          rel="noopener noreferrer"
          class="btn btn-secondary btn-sm shrink-0"
        >
          <Icon name="externalLink" size="sm" class="mr-1.5" :stroke-width="2" />
          {{ t('rechargeStore.openInNewTab') }}
        </a>
      </div>

      <div class="recharge-store-frame-shell">
        <div
          v-if="frameLoadState !== 'loaded'"
          class="recharge-store-frame-status"
          :class="{ 'recharge-store-frame-status--warning': frameLoadState !== 'loading' }"
          role="status"
          aria-live="polite"
        >
          <div class="flex items-center gap-2">
            <span v-if="frameLoadState === 'loading'" class="recharge-store-spinner" aria-hidden="true"></span>
            <Icon v-else name="infoCircle" size="sm" aria-hidden="true" />
            <span>{{ frameStatusMessage }}</span>
          </div>
          <a
            v-if="frameLoadState !== 'loading'"
            :href="RECHARGE_STORE_URL"
            target="_blank"
            rel="noopener noreferrer"
            class="btn btn-secondary btn-sm mt-2 sm:mt-0"
          >
            {{ t('rechargeStore.openInNewTab') }}
          </a>
        </div>
        <iframe
          :src="RECHARGE_STORE_URL"
          :title="t('rechargeStore.iframeTitle')"
          class="recharge-store-frame"
          referrerpolicy="no-referrer"
          allowfullscreen
          @load="handleFrameLoad"
          @error="handleFrameError"
        ></iframe>
      </div>
    </div>
  </AppLayout>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import AppLayout from '@/components/layout/AppLayout.vue'
import Icon from '@/components/icons/Icon.vue'

const RECHARGE_STORE_URL = 'https://catfk.com/shop/baiyuapi'
const { t } = useI18n()

type FrameLoadState = 'loading' | 'loaded' | 'slow' | 'error'

const frameLoadState = ref<FrameLoadState>('loading')
let slowLoadTimer: ReturnType<typeof setTimeout> | undefined

const frameStatusMessage = computed(() => {
  if (frameLoadState.value === 'error') return t('rechargeStore.loadFailed')
  if (frameLoadState.value === 'slow') return t('rechargeStore.loadSlow')
  return t('rechargeStore.loading')
})

const handleFrameLoad = () => {
  frameLoadState.value = 'loaded'
  if (slowLoadTimer) clearTimeout(slowLoadTimer)
}

const handleFrameError = () => {
  frameLoadState.value = 'error'
  if (slowLoadTimer) clearTimeout(slowLoadTimer)
}

onMounted(() => {
  slowLoadTimer = setTimeout(() => {
    if (frameLoadState.value === 'loading') frameLoadState.value = 'slow'
  }, 8000)
})

onBeforeUnmount(() => {
  if (slowLoadTimer) clearTimeout(slowLoadTimer)
})
</script>

<style scoped>
.recharge-store-layout {
  @apply flex min-h-0 flex-col gap-3;
  height: calc(100dvh - 64px - 4rem);
}

.recharge-store-toolbar {
  @apply flex shrink-0 flex-col gap-3 rounded-xl border border-gray-200 bg-white px-4 py-3 shadow-sm;
  @apply dark:border-dark-700 dark:bg-dark-800 sm:flex-row sm:items-center sm:justify-between;
}

.store-mark {
  @apply mt-0.5 flex h-7 w-7 shrink-0 items-center justify-center rounded-lg;
  @apply bg-primary-50 text-primary-600 dark:bg-primary-900/30 dark:text-primary-300;
}

.recharge-store-frame-shell {
  @apply relative min-h-0 flex-1 overflow-hidden rounded-xl border border-gray-200 bg-white shadow-sm;
  @apply dark:border-dark-700 dark:bg-dark-900;
}

.recharge-store-frame-status {
  @apply absolute inset-x-3 top-3 z-10 flex items-center justify-between gap-3 rounded-lg border border-gray-200 bg-white/95 px-3 py-2 text-sm text-gray-600 shadow-sm backdrop-blur;
  @apply dark:border-dark-600 dark:bg-dark-800/95 dark:text-dark-200;
}

.recharge-store-frame-status--warning {
  @apply border-amber-200 bg-amber-50/95 text-amber-800;
  @apply dark:border-amber-800 dark:bg-amber-950/90 dark:text-amber-200;
}

.recharge-store-spinner {
  @apply h-4 w-4 animate-spin rounded-full border-2 border-primary-200 border-t-primary-600;
}

.recharge-store-frame {
  display: block;
  width: 100%;
  height: 100%;
  min-height: 100%;
  border: 0;
  background: transparent;
}

@media (max-width: 639px) {
  .recharge-store-layout {
    height: auto;
    min-height: calc(100dvh - 6rem);
  }

  .recharge-store-toolbar {
    @apply gap-2 px-3 py-3;
  }

  .recharge-store-toolbar > a {
    @apply min-h-11 w-full justify-center;
  }

  .recharge-store-frame-shell {
    flex: none;
    height: max(720px, calc(100dvh - 10rem));
    min-height: 720px;
    overflow: auto;
    overscroll-behavior: contain;
    -webkit-overflow-scrolling: touch;
  }

  .recharge-store-frame {
    min-height: 720px;
  }

  .recharge-store-frame-status {
    @apply inset-x-2 top-2 flex-col items-stretch;
  }

  .recharge-store-frame-status .btn {
    @apply w-full justify-center;
  }
}

@media (prefers-reduced-motion: reduce) {
  .recharge-store-spinner {
    animation: none;
  }
}
</style>
