/**
 * app/composables/useBatchProgress — progreso derivado de un lote (FAD P1).
 *
 * El servidor decide los hitos; la UI re-deriva el progreso del lote EN CURSO
 * como presentación: `startedAtSim` + duración de la receta contra el simNow
 * del SimClock. Con datos incompletos (sin arranque, sin receta, reloj sin
 * anclar) cae al último progreso OBSERVADO del servidor — nunca inventa.
 */

import type { SimTime } from '~shared/simtime'
import type { ProductionBatch } from '~domain/buildings'
import type { Recipe } from '~domain/world'

/** Progreso 0–100 del lote en curso, o `null` si no es derivable. */
export function batchProgressPct(
  batch: ProductionBatch,
  recipe: Recipe | null,
  simNow: SimTime | null,
): number | null {
  if (batch.status === 'completed') {
    return 100
  }
  if (batch.status !== 'running') {
    return batch.progressPctObserved
  }
  if (
    batch.startedAtSim === null ||
    recipe === null ||
    recipe.batchSimSeconds <= 0 ||
    simNow === null
  ) {
    return batch.progressPctObserved
  }
  const pct = ((simNow - batch.startedAtSim) / recipe.batchSimSeconds) * 100
  return Math.min(100, Math.max(0, Math.floor(pct)))
}
