import { onScopeDispose, readonly, ref } from 'vue'

const now = ref(Date.now())
let consumers = 0
let timer: ReturnType<typeof setInterval> | undefined

/** One clock for relative labels; never makes the parent table tick every second. */
export function useMinuteClock() {
  consumers += 1
  if (!timer) {
    now.value = Date.now()
    timer = setInterval(() => {
      if (typeof document !== 'undefined' && document.hidden) return
      if (Math.floor(now.value / 60_000) !== Math.floor(Date.now() / 60_000)) now.value = Date.now()
    }, 1000)
  }
  onScopeDispose(() => {
    consumers -= 1
    if (!consumers && timer) {
      clearInterval(timer)
      timer = undefined
    }
  })
  return readonly(now)
}
