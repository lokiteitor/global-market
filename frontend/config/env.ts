import { z } from 'zod'

/**
 * Validación de la runtime config pública (FAD §23.7).
 *
 * Un entorno mal configurado debe fallar AL ARRANCAR con un mensaje claro,
 * nunca a mitad de sesión. El plugin `app/plugins/01.runtime-config.ts`
 * ejecuta esta validación en el arranque de servidor y de cliente.
 *
 * Los mensajes de este módulo son diagnósticos para el operador/desarrollador
 * (no UI de jugador), por eso no pasan por shared/i18n.
 */
export const publicRuntimeConfigSchema = z.object({
  /**
   * Prefijo base de la API REST (contrato docs/api/openapi.yaml v1.1.0).
   * Ruta absoluta same-origin (recomendado: Caddy/devProxy enrutan `/api`)
   * o URL http(s) completa para entornos especiales.
   */
  apiBase: z
    .string()
    .min(1, 'apiBase no puede estar vacío')
    .refine(
      (value) =>
        value.startsWith('/') || value.startsWith('http://') || value.startsWith('https://'),
      'apiBase debe ser una ruta absoluta ("/api/v1") o una URL http(s) completa',
    )
    .refine((value) => !value.endsWith('/'), 'apiBase no debe terminar en "/"'),
})

export type PublicRuntimeConfig = z.infer<typeof publicRuntimeConfigSchema>

/**
 * Valida la runtime config pública. Lanza con un mensaje accionable si el
 * entorno está mal configurado (fail-fast al iniciar).
 */
export function validatePublicRuntimeConfig(raw: unknown): PublicRuntimeConfig {
  const parsed = publicRuntimeConfigSchema.safeParse(raw)
  if (!parsed.success) {
    const issues = parsed.error.issues
      .map((issue) => `  - ${issue.path.join('.') || '(raíz)'}: ${issue.message}`)
      .join('\n')
    throw new Error(
      `[imperio-industrial] Runtime config pública inválida — el entorno está mal configurado.\n` +
        `Revisa runtimeConfig.public en nuxt.config.ts o las variables NUXT_PUBLIC_*:\n${issues}`,
    )
  }
  return parsed.data
}
