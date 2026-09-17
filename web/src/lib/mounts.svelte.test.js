import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import {
  getMounts,
  setActiveMounts,
  refreshMounts,
  handleMountStarted,
  handleMountActive,
  handleMountUnmounted,
  handleMountFailed,
} from './mounts.svelte.js'

describe('mounts store', () => {
  beforeEach(() => {
    setActiveMounts([])
  })

  afterEach(() => {
    vi.unstubAllGlobals()
  })

  it('exposes initial reactive state', () => {
    const mounts = getMounts()
    expect(mounts.list).toEqual([])
    expect(mounts.loading).toBe(false)
    expect(mounts.error).toBeNull()
  })

  it('sets active mounts list', () => {
    setActiveMounts([{ id: 1, mount_path: '/mnt/vault-fuse/mount-1' }])
    const mounts = getMounts()
    expect(mounts.list).toHaveLength(1)
    expect(mounts.list[0].id).toBe(1)
  })

  it('refreshes mounts from API successfully', async () => {
    const mockList = [
      { id: 10, mount_path: '/mnt/vault-fuse/mount-10', status: 'active' },
    ]
    const fetch = vi.fn(async () => new Response(JSON.stringify(mockList), {
      status: 200,
      headers: { 'content-type': 'application/json' },
    }))
    vi.stubGlobal('fetch', fetch)

    await refreshMounts()

    const mounts = getMounts()
    expect(mounts.list).toEqual(mockList)
    expect(mounts.loading).toBe(false)
    expect(mounts.error).toBeNull()
  })

  it('handles refresh error gracefully', async () => {
    const fetch = vi.fn(async () => new Response('{"error":"Internal error"}', {
      status: 500,
      headers: { 'content-type': 'application/json' },
    }))
    vi.stubGlobal('fetch', fetch)

    await refreshMounts()

    const mounts = getMounts()
    expect(mounts.loading).toBe(false)
    expect(mounts.error).toBe('Internal error')
  })

  it('handles mount.started event', () => {
    handleMountStarted({ session_id: 1, mount_path: '/mnt/vault-fuse/mount-1', job_id: 2 })
    const mounts = getMounts()
    expect(mounts.list).toHaveLength(1)
    expect(mounts.list[0].id).toBe(1)
    expect(mounts.list[0].status).toBe('mounting')

    // Duplicate session_id is a no-op
    handleMountStarted({ session_id: 1, mount_path: '/mnt/vault-fuse/mount-1', job_id: 2 })
    expect(mounts.list).toHaveLength(1)
  })

  it('handles mount.active event', () => {
    const session = { id: 1, mount_path: '/mnt/vault-fuse/mount-1', status: 'active', job_id: 2 }
    handleMountActive(session)
    let mounts = getMounts()
    expect(mounts.list).toHaveLength(1)
    expect(mounts.list[0].status).toBe('active')

    // Update existing session
    const updated = { ...session, status: 'active', job_name: 'test-job' }
    handleMountActive(updated)
    mounts = getMounts()
    expect(mounts.list).toHaveLength(1)
    expect(mounts.list[0].job_name).toBe('test-job')
  })

  it('handles mount.unmounted and mount.failed events', () => {
    setActiveMounts([
      { id: 1, status: 'active' },
      { id: 2, status: 'active' },
    ])
    handleMountUnmounted({ session_id: 1 })
    let mounts = getMounts()
    expect(mounts.list).toHaveLength(1)
    expect(mounts.list[0].id).toBe(2)

    handleMountFailed({ session_id: 2 })
    mounts = getMounts()
    expect(mounts.list).toHaveLength(0)
  })
})
