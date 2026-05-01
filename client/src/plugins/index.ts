/**
 * plugins/index.ts
 *
 * Automatically included in `./src/main.ts`
 */

// Plugins
import vuetify from './vuetify'
import router from '../router'
import pinia from './pinia'
import vCodeBlock from './code-block'
import i18n from './i18n'

import axios from 'axios'
import { useCookies } from 'vue3-cookies'

// Send the JWT cookie kuso.JWT_TOKEN as Authorization: Bearer on every API
// call. The server's JwtStrategy uses ExtractJwt.fromAuthHeaderAsBearerToken,
// not the cookie, so without this interceptor every authenticated request
// would 401 even after login.
const { cookies } = useCookies()
axios.interceptors.request.use(config => {
  const token = cookies.get('kuso.JWT_TOKEN')
  if (token) {
    config.headers = config.headers || {}
    config.headers['Authorization'] = 'Bearer ' + token
  }
  return config
})
// Types
import type { App } from 'vue'

export function registerPlugins (app: App) {
  app
    .use(pinia)
    // @ts-ignore: Type missmatch
    .use(vCodeBlock)
    .use(i18n)
    .use(vuetify)
    .use(router)
}
