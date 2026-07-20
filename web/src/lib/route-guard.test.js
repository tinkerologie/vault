import { describe, expect, it } from 'vitest'
import { shouldRedirectRoute } from './route-guard.js'

describe('shouldRedirectRoute', () => {
  it.each(['/jobs', '/restore', '/recovery', '/recover'])(
    'redirects replica-only access away from %s',
    (route) => {
      expect(shouldRedirectRoute({
        route,
        replicaMode: true,
        anomalyEnabled: true,
        replicationEnabled: true,
      })).toBe(true)
    },
  )

  it.each(['/jobs/42', '/restore/version-1', '/recovery/details', '/recover/step-2'])(
    'redirects nested replica-only access away from %s',
    (route) => {
      expect(shouldRedirectRoute({
        route,
        replicaMode: true,
        anomalyEnabled: true,
        replicationEnabled: true,
      })).toBe(true)
    },
  )

  it('does not overmatch a similarly named route', () => {
    expect(shouldRedirectRoute({
      route: '/jobs-old',
      replicaMode: true,
      anomalyEnabled: true,
      replicationEnabled: true,
    })).toBe(false)
  })

  it('keeps shared replica routes available', () => {
    expect(shouldRedirectRoute({
      route: '/history',
      replicaMode: true,
      anomalyEnabled: true,
      replicationEnabled: true,
    })).toBe(false)
  })

  it('redirects disabled optional daemon routes', () => {
    expect(shouldRedirectRoute({
      route: '/anomalies',
      replicaMode: false,
      anomalyEnabled: false,
      replicationEnabled: true,
    })).toBe(true)
    expect(shouldRedirectRoute({
      route: '/replication',
      replicaMode: false,
      anomalyEnabled: true,
      replicationEnabled: false,
    })).toBe(true)
  })
})
