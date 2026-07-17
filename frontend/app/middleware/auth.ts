/**
 * app/middleware/auth — guard de rutas con sesión requerida.
 *
 * Middleware NOMBRADO: las páginas protegidas optan con
 * `definePageMeta({ middleware: 'auth' })`. Sin sesión autenticada redirige
 * a /login conservando el destino en `?redirect=` para volver tras el login.
 *
 * El token vive SOLO en memoria (FAD §24.2), así que en SSR y tras un
 * hard-reload nunca hay sesión: la redirección a /login en ese caso es el
 * comportamiento diseñado. Esto es UX preventiva, no seguridad — la
 * autorización real la impone el servidor en cada petición (C7/C13).
 */

import { useSessionStore } from '../stores/session.store'

export default defineNuxtRouteMiddleware((to) => {
  const session = useSessionStore()

  if (session.isAuthenticated) {
    return
  }
  // Nunca redirigir /login sobre sí misma (evita bucles si se anota por error).
  if (to.path === '/login') {
    return
  }
  return navigateTo({ path: '/login', query: { redirect: to.fullPath } })
})
