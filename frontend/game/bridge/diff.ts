/**
 * game/bridge/diff — diffing PURO de view-models (FAD §11.6, P8).
 *
 * El bridge difunde DIFFS, no snapshots: altas/updates (`upserts`) y bajas
 * (`removes`) entre el mapa de VMs anterior y el siguiente, para que el
 * renderer haga reconciliación de sprites mínima contra pools. Igualdad
 * estructural superficial (los VMs son planos; `points` de LinkVM se compara
 * por referencia y, si difiere, por valor).
 */

/** Diff de una colección de VMs keyed por id. */
export interface VmDiff<VM> {
  /** VMs nuevos o cambiados, en el orden del mapa siguiente. */
  readonly upserts: readonly VM[]
  /** Ids que dejan de ser visibles (el renderer devuelve el sprite al pool). */
  readonly removes: readonly string[]
}

export const EMPTY_DIFF: VmDiff<never> = { upserts: [], removes: [] }

function pointsEqual(a: unknown, b: unknown): boolean {
  if (a === b) {
    return true
  }
  if (!Array.isArray(a) || !Array.isArray(b) || a.length !== b.length) {
    return false
  }
  for (let i = 0; i < a.length; i += 1) {
    const pa = a[i] as unknown
    const pb = b[i] as unknown
    if (!Array.isArray(pa) || !Array.isArray(pb) || pa[0] !== pb[0] || pa[1] !== pb[1]) {
      return false
    }
  }
  return true
}

/**
 * Igualdad superficial entre dos VMs planos: primitivas por `===`, arrays de
 * puntos por valor. Suficiente para las formas de game/bridge/vm (los VMs no
 * anidan objetos).
 */
export function vmEquals<VM extends object>(a: VM, b: VM): boolean {
  const recordA = a as Record<string, unknown>
  const recordB = b as Record<string, unknown>
  for (const key of Object.keys(recordA)) {
    const va = recordA[key]
    const vb = recordB[key]
    if (va === vb) {
      continue
    }
    if (Array.isArray(va) && Array.isArray(vb) && pointsEqual(va, vb)) {
      continue
    }
    return false
  }
  return true
}

/** Diff prev → next. Determinista: `upserts` en orden de `next`, `removes` en orden de `prev`. */
export function diffVms<VM extends object>(
  prev: ReadonlyMap<string, VM>,
  next: ReadonlyMap<string, VM>,
): VmDiff<VM> {
  const upserts: VM[] = []
  for (const [id, vm] of next) {
    const before = prev.get(id)
    if (before === undefined || !vmEquals(before, vm)) {
      upserts.push(vm)
    }
  }
  const removes: string[] = []
  for (const id of prev.keys()) {
    if (!next.has(id)) {
      removes.push(id)
    }
  }
  return { upserts, removes }
}
