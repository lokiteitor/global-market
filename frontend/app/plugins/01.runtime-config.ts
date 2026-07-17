import { validatePublicRuntimeConfig } from '../../config/env'

/**
 * Valida la runtime config pública con zod al arrancar (FAD §23.7).
 * Corre en servidor y en cliente antes que el resto de plugins (prefijo 01.):
 * un entorno mal configurado aborta el arranque con un mensaje accionable
 * en lugar de fallar a mitad de sesión.
 */
export default defineNuxtPlugin(() => {
  const runtimeConfig = useRuntimeConfig()
  validatePublicRuntimeConfig(runtimeConfig.public)
})
