/**
 * domain/auth — tipos de dominio de sesión e identidad (FAD §9.1 "Session & Identity").
 *
 * Modelo de dominio del cliente para /auth/*: los DTO crudos del contrato
 * (SessionCreated, Account) NUNCA salen de network/ (O5); esta es la forma
 * que sí cruza hacia stores y UI. Los ids llevan brand por entidad (§20.6)
 * derivado de los schemas nominales del contrato (AccountId, SessionId).
 *
 * Los instantes de sesión (`expiresAtMs`, `createdAtMs`) son wall-clock en ms
 * de epoch: la sesión es la única capa donde el wall-clock es regla legítima
 * (contrato /auth/sessions); nada de esto es sim-time.
 */

import type { EntityId } from '~shared/ids'

/** UUIDv7 de corporación (schema nominal `AccountId` del contrato). */
export type AccountId = EntityId<'Account'>

/** UUIDv7 de sesión (schema nominal `SessionId` del contrato). */
export type SessionId = EntityId<'Session'>

/** Tipo de actor. El motor no distingue el origen de un comando (ADR-010). */
export const ACCOUNT_KINDS = ['human', 'bot', 'city', 'system'] as const
export type AccountKind = (typeof ACCOUNT_KINDS)[number]

export const ACCOUNT_STATUSES = ['active', 'suspended', 'retired'] as const
export type AccountStatus = (typeof ACCOUNT_STATUSES)[number]

export const BOT_ARCHETYPES = [
  'primary_producer',
  'industrial_transformer',
  'arbitrageur',
  'freighter',
] as const
export type BotArchetype = (typeof BOT_ARCHETYPES)[number]

/** Corporación del mundo (humano, bot, ciudad o cuenta de sistema). */
export interface Account {
  readonly id: AccountId
  readonly kind: AccountKind
  readonly name: string
  readonly status: AccountStatus
  /** Solo presente cuando `kind = bot`. */
  readonly botArchetype: BotArchetype | null
  /** Alta de la cuenta, wall-clock (ms epoch) — dato de sesión/UI, no de juego. */
  readonly createdAtMs: number
}

/** Sesión autenticada: token bearer efímero (SOLO memoria, FAD §24.2). */
export interface AuthSession {
  readonly sessionId: SessionId
  readonly token: string
  /** Expiración del token, wall-clock (ms epoch). */
  readonly expiresAtMs: number
  readonly account: Account
}

export function isAccountKind(value: string): value is AccountKind {
  return (ACCOUNT_KINDS as readonly string[]).includes(value)
}

export function isAccountStatus(value: string): value is AccountStatus {
  return (ACCOUNT_STATUSES as readonly string[]).includes(value)
}

export function isBotArchetype(value: string): value is BotArchetype {
  return (BOT_ARCHETYPES as readonly string[]).includes(value)
}
