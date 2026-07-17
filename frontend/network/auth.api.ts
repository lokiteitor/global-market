/**
 * network/auth.api — puerto AuthApi contra /auth/* del contrato (FAD §9.1, §9.5).
 *
 * Los DTO crudos (`SessionCreated`, `Account`) se mapean AQUÍ a los tipos de
 * dominio de `~domain/auth`; no cruzan esta frontera (O5). Los mappers validan
 * el formato de los UUID al entrar (FAD §9.5) y la pertenencia de los enums:
 * un payload fuera de contrato produce AppError `protocol`, nunca un estado
 * silenciosamente corrupto.
 *
 * La store de sesión consume el PUERTO `AuthApi` (inyectado por el plugin de
 * red), no este módulo directamente: en tests se dobla el puerto.
 */

import type { Account, AccountId, AuthSession, SessionId } from '~domain/auth'
import { isAccountKind, isAccountStatus, isBotArchetype } from '~domain/auth'
import { parseEntityId } from '~shared/ids'
import type { components } from '../types/api'
import type { RestClient } from './rest'
import { appErrorFromProtocol } from './rest/errors'

type AccountDto = components['schemas']['Account']
type SessionCreatedDto = components['schemas']['SessionCreated']
type SessionCreateRequestDto = components['schemas']['SessionCreateRequest']

/** Puerto de autenticación que consume la capa de aplicación (store de sesión). */
export interface AuthApi {
  /** POST /auth/sessions — el token se devuelve UNA única vez. */
  createSession(accountName: string, secret: string): Promise<AuthSession>
  /** DELETE /auth/sessions/current — invalida la sesión en el servidor. */
  deleteCurrentSession(): Promise<void>
  /** GET /auth/me — corporación asociada a la sesión actual. */
  getCurrentAccount(): Promise<Account>
}

function mapAccount(dto: AccountDto): Account {
  const id = parseEntityId<'Account'>(dto.id)
  if (!id.ok) {
    throw appErrorFromProtocol(`Account.id no es un UUID válido ("${dto.id}")`)
  }
  if (!isAccountKind(dto.kind)) {
    throw appErrorFromProtocol(`Account.kind desconocido ("${String(dto.kind)}")`)
  }
  if (!isAccountStatus(dto.status)) {
    throw appErrorFromProtocol(`Account.status desconocido ("${String(dto.status)}")`)
  }
  const createdAtMs = Date.parse(dto.created_at)
  if (Number.isNaN(createdAtMs)) {
    throw appErrorFromProtocol(`Account.created_at no es date-time ("${dto.created_at}")`)
  }
  let botArchetype: Account['botArchetype'] = null
  if (dto.bot_archetype !== undefined) {
    if (!isBotArchetype(dto.bot_archetype)) {
      throw appErrorFromProtocol(
        `Account.bot_archetype desconocido ("${String(dto.bot_archetype)}")`,
      )
    }
    botArchetype = dto.bot_archetype
  }
  return {
    id: id.value satisfies AccountId,
    kind: dto.kind,
    name: dto.name,
    status: dto.status,
    botArchetype,
    createdAtMs,
  }
}

function mapSessionCreated(dto: SessionCreatedDto): AuthSession {
  const sessionId = parseEntityId<'Session'>(dto.session_id)
  if (!sessionId.ok) {
    throw appErrorFromProtocol(`SessionCreated.session_id no es un UUID válido`)
  }
  if (typeof dto.token !== 'string' || dto.token.length === 0) {
    throw appErrorFromProtocol('SessionCreated.token vacío o ausente')
  }
  const expiresAtMs = Date.parse(dto.expires_at)
  if (Number.isNaN(expiresAtMs)) {
    throw appErrorFromProtocol(`SessionCreated.expires_at no es date-time ("${dto.expires_at}")`)
  }
  return {
    sessionId: sessionId.value satisfies SessionId,
    token: dto.token,
    expiresAtMs,
    account: mapAccount(dto.account),
  }
}

export function createAuthApi(rest: RestClient): AuthApi {
  return {
    async createSession(accountName, secret) {
      const body: SessionCreateRequestDto = { account_name: accountName, secret }
      const { data } = await rest.request<SessionCreatedDto>({
        method: 'POST',
        path: '/auth/sessions',
        body,
      })
      return mapSessionCreated(data)
    },

    async deleteCurrentSession() {
      await rest.requestVoid({ method: 'DELETE', path: '/auth/sessions/current' })
    },

    async getCurrentAccount() {
      const { data } = await rest.request<AccountDto>({ method: 'GET', path: '/auth/me' })
      return mapAccount(data)
    },
  }
}
