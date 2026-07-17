/**
 * app/composables/useAppError — AppError → mensaje de UI (FAD §13.7).
 *
 * Diccionario único error.code → texto: la clave i18n `error.<code>` del
 * catálogo (shared/i18n) con fallback genérico. Los fallos de transporte
 * (sin respuesta HTTP) y los errores no tipados tienen texto propio para no
 * culpar al servidor de lo que es la red o el cliente.
 */

import { t, tErrorCode } from '~shared/i18n'
import { AppError } from '~network/rest'

export function useAppError() {
  /** Mensaje de UI para cualquier error capturado en una acción/caso de uso. */
  function messageFor(error: unknown): string {
    if (error instanceof AppError) {
      if (error.kind === 'network') {
        return t('error.NETWORK')
      }
      // Códigos catalogados → texto propio; futuros → fallback con el código.
      return tErrorCode(error.rawCode ?? error.code)
    }
    return t('error.CLIENT')
  }

  /** ¿Es la ventana de mantenimiento? (overlay frozen, no toast de error — FAD §12.9). */
  function isMaintenance(error: unknown): boolean {
    return error instanceof AppError && error.isMaintenance
  }

  return { messageFor, isMaintenance }
}
