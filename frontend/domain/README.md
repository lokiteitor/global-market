# domain/ — Domain Layer (FAD §8, §10.2)

Modelos de dominio, value-objects y máquinas de estado de UI del cliente.
**Framework-agnostic**: aquí no entra Vue, Nuxt, Pinia ni Phaser (regla de
linter `imperio/kernel-boundaries`), y se testea sin montar la app.

Se puebla en los incrementos de features (stores/casos de uso). Alias: `~domain`.
