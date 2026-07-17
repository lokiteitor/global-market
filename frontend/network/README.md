# network/ — Networking Layer (FAD §8, §10.2, §12)

Cliente REST del contrato, puerto `NetworkTransport` con sus adaptadores
(`GatewayTransportAdapter` como ACL del Notification/Event Gateway) y los
mappers DTO↔dominio. **Los DTO crudos del servidor no salen de esta capa**
(O5); prohibido importar de `app/` (regla de linter `imperio/network-boundaries`).

Se puebla en el incremento de networking (FE 4). Alias: `~network`.
Los tipos del contrato se generan con `npm run gen:api` en `types/api.d.ts`.
