import { afterEach, describe, expect, it, vi } from 'vitest'
import { render } from '@testing-library/svelte'
import { fireEvent } from '@testing-library/dom'
import { createRawSnippet } from 'svelte'
import * as runtimeConfig from '../lib/runtime-config.js'
import PullToRefresh from './PullToRefresh.svelte'

const children = createRawSnippet(() => ({ render: () => '<p>content</p>' }))

async function pull(container, distance) {
  const root = container.firstElementChild
  await fireEvent.touchStart(root, { touches: [{ clientY: 0 }] })
  await fireEvent.touchMove(root, { touches: [{ clientY: distance }] })
  await fireEvent.touchEnd(root)
}

afterEach(() => vi.restoreAllMocks())

describe('PullToRefresh', () => {
  it('refreshes on a pull in standalone', async () => {
    const onrefresh = vi.fn(async () => {})
    const { container } = render(PullToRefresh, { onrefresh, children })
    await pull(container, 400)
    expect(onrefresh).toHaveBeenCalledOnce()
  })

  it('ignores the pull when embedded', async () => {
    vi.spyOn(runtimeConfig, 'isEmbedded').mockReturnValue(true)
    const onrefresh = vi.fn(async () => {})
    const { container } = render(PullToRefresh, { onrefresh, children })
    await pull(container, 400)
    expect(onrefresh).not.toHaveBeenCalled()
  })
})
