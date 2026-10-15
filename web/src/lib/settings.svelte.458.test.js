import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

// Feature-flag store tests (issue #458). api.js is mocked so the settings and
// replication-source responses can be scripted per case.

const api = vi.hoisted(() => ({
  getSettings: vi.fn(),
  listReplicationSources: vi.fn(),
}))
vi.mock('./api.js', () => ({ api }))

let settings

beforeEach(async () => {
  vi.resetModules()
  api.getSettings.mockReset()
  api.listReplicationSources.mockReset()
  vi.spyOn(console, 'warn').mockImplementation(() => {})
  settings = await import('./settings.svelte.js')
})

afterEach(() => vi.restoreAllMocks())

describe('feature flags', () => {
  it('starts with optimistic defaults and not loaded', () => {
    expect(settings.getAnomalyEnabled()).toBe(true)
    expect(settings.getReplicationEnabled()).toBe(true)
    expect(settings.getFeatureFlagsLoaded()).toBe(false)
  })

  it('setters coerce to booleans', () => {
    settings.setAnomalyEnabled(0)
    settings.setReplicationEnabled('')
    expect(settings.getAnomalyEnabled()).toBe(false)
    expect(settings.getReplicationEnabled()).toBe(false)
    settings.setAnomalyEnabled('yes')
    settings.setReplicationEnabled(1)
    expect(settings.getAnomalyEnabled()).toBe(true)
    expect(settings.getReplicationEnabled()).toBe(true)
  })

  it('fails open when the settings fetch fails', async () => {
    settings.setAnomalyEnabled(false)
    settings.setReplicationEnabled(false)
    api.getSettings.mockRejectedValue(new Error('down'))
    await settings.loadFeatureFlags()
    expect(settings.getAnomalyEnabled()).toBe(true)
    expect(settings.getReplicationEnabled()).toBe(true)
    expect(settings.getFeatureFlagsLoaded()).toBe(true)
    expect(console.warn).toHaveBeenCalled()
  })

  it('honours explicit true/false settings without listing sources', async () => {
    api.getSettings.mockResolvedValue({ anomaly_detection_enabled: 'false', replication_enabled: 'false' })
    await settings.loadFeatureFlags()
    expect(settings.getAnomalyEnabled()).toBe(false)
    expect(settings.getReplicationEnabled()).toBe(false)
    expect(api.listReplicationSources).not.toHaveBeenCalled()

    api.getSettings.mockResolvedValue({ replication_enabled: 'true' })
    await settings.loadFeatureFlags()
    expect(settings.getAnomalyEnabled()).toBe(true)
    expect(settings.getReplicationEnabled()).toBe(true)
  })

  it('derives replication from existing sources when the setting is unset', async () => {
    api.getSettings.mockResolvedValue({})
    api.listReplicationSources.mockResolvedValue([{ id: 1 }])
    await settings.loadFeatureFlags()
    expect(settings.getReplicationEnabled()).toBe(true)

    api.listReplicationSources.mockResolvedValue([])
    await settings.loadFeatureFlags()
    expect(settings.getReplicationEnabled()).toBe(false)

    api.getSettings.mockResolvedValue(null)
    api.listReplicationSources.mockResolvedValue(null)
    await settings.loadFeatureFlags()
    expect(settings.getReplicationEnabled()).toBe(false)
    expect(settings.getAnomalyEnabled()).toBe(true)
  })

  it('hides replication when the sources fetch fails', async () => {
    api.getSettings.mockResolvedValue({})
    api.listReplicationSources.mockRejectedValue(new Error('x'))
    await settings.loadFeatureFlags()
    expect(settings.getReplicationEnabled()).toBe(false)
    expect(settings.getFeatureFlagsLoaded()).toBe(true)
  })
})
