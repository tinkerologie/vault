import './app.css'
import App from './App.svelte'
import { mount } from 'svelte'
import { isEmbedded } from './lib/runtime-config.js'
import { startEmbeddedPresentation } from './lib/embed.js'

if (isEmbedded()) startEmbeddedPresentation()

const app = mount(App, { target: document.getElementById('app') })

export default app
