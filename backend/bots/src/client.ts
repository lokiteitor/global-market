/**
 * Cliente REST tipado mínimo contra la API pública del gateway.
 *
 * - Login y re-login transparente ante 401 (token de sesión bearer).
 * - Envoltorios { data, meta } / { error } de specs/openapi.yaml.
 * - Backoff en 429/503 respetando la cabecera Retry-After (nunca en tromba).
 * - Idempotency-Key (crypto.randomUUID) en todo POST: la clave se genera UNA
 *   vez por comando lógico y se reutiliza en los reintentos internos.
 *
 * Sin dependencias de runtime: fetch global de Node 22.
 */

import { randomUUID } from "node:crypto";
import { GATEWAY_URL } from "./config.js";
import type { ApiErrorBody, Meta, SessionCreated } from "./types.js";

export class ApiError extends Error {
  constructor(
    readonly status: number,
    readonly code: string,
    message: string,
    readonly details?: Record<string, unknown>,
  ) {
    super(`[${status} ${code}] ${message}`);
    this.name = "ApiError";
  }
}

export interface Credentials {
  readonly accountName: string;
  readonly secret: string;
}

export type QueryParams = Record<string, string | number | boolean | undefined>;

const MAX_BACKOFF_RETRIES = 2;
const DEFAULT_BACKOFF_MS = 2000;

function sleep(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

export class ApiClient {
  private token: string | null = null;
  /** UUID de la cuenta autenticada (disponible tras login). */
  accountId = "";

  constructor(
    private readonly creds: Credentials,
    private readonly baseUrl: string = GATEWAY_URL,
  ) {}

  async login(): Promise<void> {
    const res = await fetch(`${this.baseUrl}/auth/sessions`, {
      method: "POST",
      headers: { "content-type": "application/json" },
      body: JSON.stringify({
        account_name: this.creds.accountName,
        secret: this.creds.secret,
      }),
    });
    const body = (await res.json()) as { data: SessionCreated } & ApiErrorBody;
    if (!res.ok) {
      const err = body.error ?? { code: "INTERNAL", message: "login failed" };
      throw new ApiError(res.status, err.code, err.message, err.details);
    }
    this.token = body.data.token;
    this.accountId = body.data.account.id;
  }

  private async request<T>(
    method: "GET" | "POST" | "PATCH" | "DELETE",
    path: string,
    body?: unknown,
  ): Promise<{ data: T; meta: Meta }> {
    // Una clave de idempotencia por comando lógico, estable entre reintentos.
    const idemKey = method === "POST" ? randomUUID() : null;
    let reloggedIn = false;
    let backoffs = 0;

    for (;;) {
      if (this.token === null) await this.login();

      const headers: Record<string, string> = {
        authorization: `Bearer ${this.token}`,
      };
      if (body !== undefined) headers["content-type"] = "application/json";
      if (idemKey !== null) headers["idempotency-key"] = idemKey;

      const res = await fetch(this.baseUrl + path, {
        method,
        headers,
        body: body === undefined ? undefined : JSON.stringify(body),
      });

      if (res.status === 401 && !reloggedIn) {
        // Sesión expirada: re-login una única vez y reintento.
        reloggedIn = true;
        this.token = null;
        continue;
      }

      if ((res.status === 429 || res.status === 503) && backoffs < MAX_BACKOFF_RETRIES) {
        backoffs += 1;
        const retryAfter = Number(res.headers.get("retry-after"));
        const waitMs = Number.isFinite(retryAfter) && retryAfter > 0
          ? retryAfter * 1000
          : DEFAULT_BACKOFF_MS * backoffs;
        await sleep(waitMs);
        continue;
      }

      if (res.status === 204) {
        return { data: undefined as T, meta: { sim_time: "", server_time: "" } };
      }

      const parsed = (await res.json()) as ({ data: T; meta: Meta } & ApiErrorBody);
      if (!res.ok) {
        const err = parsed.error ?? { code: "INTERNAL", message: res.statusText };
        throw new ApiError(res.status, err.code, err.message, err.details);
      }
      return { data: parsed.data, meta: parsed.meta };
    }
  }

  get<T>(path: string, params?: QueryParams): Promise<{ data: T; meta: Meta }> {
    let qs = "";
    if (params) {
      const search = new URLSearchParams();
      for (const [k, v] of Object.entries(params)) {
        if (v !== undefined) search.set(k, String(v));
      }
      const s = search.toString();
      if (s.length > 0) qs = `?${s}`;
    }
    return this.request<T>("GET", path + qs);
  }

  post<T>(path: string, body: unknown): Promise<{ data: T; meta: Meta }> {
    return this.request<T>("POST", path, body);
  }
}
