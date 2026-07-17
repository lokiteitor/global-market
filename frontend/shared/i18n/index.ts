/**
 * shared/i18n — textos de UI externalizados (kernel, sin dependencias).
 *
 * ÚNICA fuente de strings de la interfaz: ningún componente hardcodea texto.
 * Las claves son planas ("modulo.elemento") y el tipado de `t()` deriva del
 * propio JSON, así que una clave inexistente es un error de compilación.
 *
 * Nota de diseño: módulo mínimo deliberado mientras el único idioma sea es-ES.
 * Cuando exista más de un locale se sustituirá por una librería i18n completa
 * conservando estas claves como catálogo base.
 */

import messages from './locales/es.json'

/** Claves de texto disponibles (derivadas del catálogo es-ES). */
export type MessageKey = keyof typeof messages

/**
 * Parámetros de interpolación `{nombre}`. Los importes Money/Quantity deben
 * llegar YA formateados (shared/money#format) — aquí solo se interpolan.
 */
export type MessageParams = Readonly<Record<string, string | number>>

/** Códigos de error estables documentados por el contrato (Error.code). */
const ERROR_KEY_PREFIX = 'error.'

/**
 * Resuelve una clave de texto con interpolación opcional de `{parametros}`.
 * Un placeholder sin parámetro correspondiente se deja tal cual (visible en
 * revisión de UI, en lugar de fallar en runtime).
 */
export function t(key: MessageKey, params?: MessageParams): string {
  const template: string = messages[key]
  if (params === undefined) {
    return template
  }
  return template.replace(/\{(\w+)\}/g, (placeholder, name: string) => {
    const value = params[name]
    return value === undefined ? placeholder : String(value)
  })
}

/**
 * Traduce un `error.code` del backend (FAD §13.7) a texto de UI.
 * Códigos no catalogados caen en `error.UNKNOWN` mostrando el código estable.
 */
export function tErrorCode(code: string): string {
  const candidate = `${ERROR_KEY_PREFIX}${code}`
  if (isMessageKey(candidate)) {
    return t(candidate)
  }
  return t('error.UNKNOWN', { code })
}

/** Guarda de tipo sobre el catálogo. */
export function isMessageKey(key: string): key is MessageKey {
  return Object.hasOwn(messages, key)
}
