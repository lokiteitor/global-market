/**
 * middleware/auth.ts — guard de sesión para rutas de juego (/play, /lobby, /settings).
 *
 * SIMPLIFICACIÓN v1 (aceptada): el token vive en memoria + sessionStorage
 * (solo cliente), por lo que el guard SOLO puede decidir en cliente; en SSR
 * se deja pasar y el cliente redirige al hidratar. El plugin
 * 02.network.client (que corre ANTES que este middleware) ya restauró la
 * sesión respaldada; aquí solo se comprueba el resultado.
 */
import { useSessionStore } from '~/stores/session.store'

export default defineNuxtRouteMiddleware(() => {
  // El token es client-only: en servidor no hay forma legítima de decidir.
  if (import.meta.server) return

  const session = useSessionStore()
  // Redundante con el plugin en el arranque normal, pero cubre navegaciones
  // tempranas y tests: restaurar es idempotente.
  if (!session.isAuthenticated) session.restore()
  if (!session.isAuthenticated) {
    return navigateTo('/login')
  }
})
