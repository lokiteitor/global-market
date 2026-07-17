// Package ledger implementa el bounded context contable del backend: lectura
// de cuentas y extractos del ledger de doble entrada (contrato /ledger/*) y
// las primitivas de asiento (Poster) sobre las que se construyen las
// operaciones económicas.
//
// Las invariantes de dinero/stock viven en la base de datos (0004_ledger:
// triggers de saldo, doble entrada diferida, no-negatividad e inmutabilidad;
// regla de oro GDD 18.3/ADR-005): este módulo orquesta, la base garantiza.
//
// El acceso a datos es código generado por sqlc (ADR-020: sqlc SOLO codegen
// de queries, nunca del esquema) en el subpaquete sqlcgen, a partir de
// queries/ledger.sql y del esquema real de db/migrations.
package ledger

// La versión de sqlc queda fijada aquí para que `make backend-generate` sea
// reproducible sin añadir dependencias a go.mod (se ejecuta con go run
// paquete@versión). El código generado en sqlcgen/ SE VERSIONA.
//
//go:generate go run github.com/sqlc-dev/sqlc/cmd/sqlc@v1.30.0 generate -f ../../sqlc.yaml
