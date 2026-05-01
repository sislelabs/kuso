/**
 * main.ts
 *
 * Bootstraps Vuetify and other plugins then mounts the App`
 */

// Plugins
import { registerPlugins } from '@/plugins'

// Components
import App from './App.vue'

// Composables
import { createApp } from 'vue'

// Stores + socket bootstrap. We initialise the socket BEFORE mounting so that
// any component reading useKusoStore().kuso.socket at module-eval time
// (rather than inside setup()) gets a non-null value. Previously this was
// done in layouts/default/Default.vue, which loads after route components,
// triggering "Cannot read properties of null (reading 'on')" on first render.
import { useKusoStore } from './stores/kuso'
import { useSocketIO } from './socket.io'
import { useCookies } from 'vue3-cookies'

const app = createApp(App)
//app.config.performance = true

registerPlugins(app)

const { cookies } = useCookies()
const { socket } = useSocketIO(cookies.get('kuso.JWT_TOKEN') || '')
useKusoStore().kuso.socket = socket

app.mount('#app')
