import { onUnmounted, readonly, ref, toValue, watch, type MaybeRefOrGetter } from 'vue'

const now = ref(Date.now())
let subscriberCount = 0
let timer: ReturnType<typeof setInterval> | null = null
let isVisibilityListenerAttached = false

const canTick = () => typeof document === 'undefined' || !document.hidden

function stopTicker() {
  if (timer === null) return
  clearInterval(timer)
  timer = null
}

function startTicker() {
  if (timer !== null || subscriberCount === 0 || !canTick()) return
  timer = setInterval(() => {
    now.value = Date.now()
  }, 1000)
}

function onVisibilityChange() {
  if (!canTick()) {
    stopTicker()
    return
  }
  now.value = Date.now()
  startTicker()
}

function attachVisibilityListener() {
  if (isVisibilityListenerAttached || typeof document === 'undefined') return
  document.addEventListener('visibilitychange', onVisibilityChange)
  isVisibilityListenerAttached = true
}

function detachVisibilityListener() {
  if (!isVisibilityListenerAttached || typeof document === 'undefined') return
  document.removeEventListener('visibilitychange', onVisibilityChange)
  isVisibilityListenerAttached = false
}

function subscribe() {
  subscriberCount += 1
  if (subscriberCount === 1) attachVisibilityListener()
  now.value = Date.now()
  startTicker()
}

function unsubscribe() {
  subscriberCount = Math.max(0, subscriberCount - 1)
  if (subscriberCount > 0) return
  stopTicker()
  detachVisibilityListener()
}

/**
 * Share one TTFT countdown clock across all visible status badges. The timer
 * stops while the tab is hidden and when no badge currently needs it.
 */
export function useTTFTGuardTicker(active: MaybeRefOrGetter<boolean>) {
  let subscribed = false

  const stopWatching = watch(
    () => toValue(active),
    (shouldSubscribe) => {
      if (shouldSubscribe && !subscribed) {
        subscribed = true
        subscribe()
      } else if (!shouldSubscribe && subscribed) {
        subscribed = false
        unsubscribe()
      }
    },
    { immediate: true }
  )

  onUnmounted(() => {
    stopWatching()
    if (subscribed) {
      subscribed = false
      unsubscribe()
    }
  })

  return readonly(now)
}
