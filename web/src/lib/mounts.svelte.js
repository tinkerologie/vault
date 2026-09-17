/**
 * Shared mount state – rune-backed, survives page navigations.
 *
 * WS handlers and REST calls update this state; components import getMounts()
 * and read reactive state via the returned getters.
 */

import { SvelteDate } from 'svelte/reactivity'
import { api } from './api.js'

/** @type {any[]} */
let activeMounts = $state([])
let loading = $state(false)
let error = $state(null)

export function getMounts() {
  return {
    get list() { return activeMounts },
    get loading() { return loading },
    get error() { return error },
  }
}

export function setActiveMounts(list) {
  activeMounts = list || []
}

export async function refreshMounts() {
  loading = true
  error = null
  try {
    const list = await api.listMounts(true)
    activeMounts = list || []
  } catch (err) {
    error = err.message
  } finally {
    loading = false
  }
}

export function handleMountStarted(data) {
  if (!data?.session_id) return
  const exists = activeMounts.some(m => m.id === data.session_id)
  if (!exists) {
    activeMounts = [
      {
        id: data.session_id,
        mount_path: data.mount_path,
        job_id: data.job_id,
        status: 'mounting',
        started_at: new SvelteDate().toISOString(),
      },
      ...activeMounts,
    ]
  }
}

export function handleMountActive(session) {
  if (!session?.id) return
  const idx = activeMounts.findIndex(m => m.id === session.id)
  if (idx >= 0) {
    activeMounts = activeMounts.map(m => (m.id === session.id ? session : m))
  } else {
    activeMounts = [session, ...activeMounts]
  }
}

export function handleMountUnmounted(data) {
  if (!data?.session_id) return
  activeMounts = activeMounts.filter(m => m.id !== data.session_id)
}

export function handleMountFailed(data) {
  if (!data?.session_id) return
  activeMounts = activeMounts.filter(m => m.id !== data.session_id)
}
