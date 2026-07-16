// Integración ligera contra la BD real (semilla de seed_world.sql). Se salta
// si la base no es alcanzable para que la suite unitaria siga siendo verde.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { buildApp } from '../src/app.js';
import { createPool } from '../src/db.js';

const DB_URL = process.env.DATABASE_URL ?? 'postgres://imperio:imperio@localhost:5440/imperio';

async function dbReachable(): Promise<boolean> {
  const pool = createPool(DB_URL);
  try {
    await pool.query('SELECT 1');
    return true;
  } catch {
    return false;
  } finally {
    await pool.end();
  }
}

test('integración: auth + catálogos + envoltura', async (t) => {
  if (!(await dbReachable())) {
    t.skip('base de datos no alcanzable');
    return;
  }
  const app = await buildApp({ databaseUrl: DB_URL });
  t.after(async () => {
    await app.close();
  });

  // 401 sin token
  const noAuth = await app.inject({ method: 'GET', url: '/api/v1/world/products' });
  assert.equal(noAuth.statusCode, 401);
  assert.equal(noAuth.json().error.code, 'UNAUTHORIZED');

  // Sesión con las credenciales del seed
  const login = await app.inject({
    method: 'POST',
    url: '/api/v1/auth/sessions',
    payload: { account_name: 'Aurora Corp', secret: 'aurora' },
  });
  assert.equal(login.statusCode, 201);
  const loginBody = login.json();
  assert.ok(loginBody.data.token);
  assert.match(loginBody.meta.sim_time, /^[0-9]{1,4}-[0-9]{3}-[0-9]{2}:[0-9]{2}$/);
  const token = loginBody.data.token as string;
  const auth = { authorization: `Bearer ${token}` };

  // /auth/me
  const me = await app.inject({ method: 'GET', url: '/api/v1/auth/me', headers: auth });
  assert.equal(me.statusCode, 200);
  assert.equal(me.json().data.name, 'Aurora Corp');

  // Catálogos del seed
  const products = await app.inject({ method: 'GET', url: '/api/v1/world/products', headers: auth });
  assert.equal(products.statusCode, 200);
  assert.equal(products.json().data.length, 6);
  const someProduct = products.json().data[0];
  assert.equal(typeof someProduct.base_price, 'string');

  const regions = await app.inject({ method: 'GET', url: '/api/v1/world/regions', headers: auth });
  assert.equal(regions.json().data.length, 4);

  // Cuentas del ledger propias (al menos la caja del seed)
  const accounts = await app.inject({ method: 'GET', url: '/api/v1/ledger/accounts', headers: auth });
  assert.equal(accounts.statusCode, 200);
  assert.ok(accounts.json().data.some((a: { kind: string }) => a.kind === 'cash'));

  // UUID malformado en path → 404 NOT_FOUND
  const bad = await app.inject({ method: 'GET', url: '/api/v1/world/regions/not-a-uuid', headers: auth });
  assert.equal(bad.statusCode, 404);
  assert.equal(bad.json().error.code, 'NOT_FOUND');

  // Cierre de sesión
  const logout = await app.inject({
    method: 'DELETE',
    url: '/api/v1/auth/sessions/current',
    headers: auth,
  });
  assert.equal(logout.statusCode, 204);
  const afterLogout = await app.inject({ method: 'GET', url: '/api/v1/auth/me', headers: auth });
  assert.equal(afterLogout.statusCode, 401);
});
