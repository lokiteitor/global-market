// Package catalog implementa el lado de LECTURA del mundo estático y los
// catálogos del bounded context world (contrato world/* del OpenAPI v1.2.0):
// regiones, productos, tipos de edificio, recetas, yacimientos, ciudades y su
// curva de demanda vigente.
//
// Todo aquí es observable por cualquier corporación autenticada: la
// autorización es la sesión del gateway (RequireAuth), sin filtro de propiedad
// — los catálogos y el estado físico del mundo son información pública del
// juego (a diferencia de /ledger o /world/concessions, que sí filtran por
// titular). El módulo no muta estado: no escribe en la BD.
//
// Fronteras (SAD §7): world no importa contracts/market/auth ni viceversa;
// solo platform y sim son plataforma compartida. El acceso a datos es código
// generado por sqlc (ADR-020: solo codegen de queries, nunca del esquema) en
// el paquete compartido del contexto internal/world/sqlcgen, a partir de
// internal/world/queries/*.sql y del esquema real de db/migrations. La
// generación la dispara la directiva go:generate compartida del backend
// (internal/ledger/generate.go → sqlc.yaml).
//
// Serialización del contrato: dinero y stock son int64 de punto fijo (string
// en el JSON, jamás float); el sim-time es int64; supply_index y
// saturation_factor son números del contrato (float64, no dinero); las
// geometrías (bounds, location) son objetos GeoJSON con coordenadas planas de
// mundo en metros [x_m, y_m] (SRID 0, ADR-019) proyectadas con ST_AsGeoJSON,
// nunca lon/lat.
package catalog
