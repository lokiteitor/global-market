/**
 * game/road-raster.ts — rasterizado de enlaces logísticos a celdas de carretera.
 *
 * Puro TS sin Phaser (testeable en node). Convierte los LineString de los
 * links (en coordenadas de tile continuas) en celdas de la grilla iso con el
 * frame pak128 correcto según conexiones: la máscara NSEW de cada celda se
 * calcula GLOBALMENTE sobre todos los links visibles, de modo que dos links
 * que atraviesan la misma celda producen el frame de cruce/T correspondiente.
 *
 * Convención de ejes ↔ Simutrans: v− = N, v+ = S, u+ = E, u− = W.
 */
import type { TilePoint } from '~/lib/kernel/projection'

export interface GridCell {
  u: number
  v: number
}

export interface RoadCell extends GridCell {
  /** Frame del atlas pak128 (p. ej. 'road.ns', 'road.nsew'). */
  frame: string
}

export interface RasterLink {
  id: string
  /** LineString del link en coordenadas lon/lat (GeoJSON). */
  coords: readonly [number, number][]
}

const N = 1
const S = 2
const E = 4
const W = 8

const OPPOSITE: Record<number, number> = { [N]: S, [S]: N, [E]: W, [W]: E }

const FRAME_BY_MASK: Record<number, string> = {
  0: 'road.dead',
  [N]: 'road.n',
  [S]: 'road.s',
  [E]: 'road.e',
  [W]: 'road.w',
  [N | S]: 'road.ns',
  [E | W]: 'road.ew',
  [N | E]: 'road.ne',
  [S | E]: 'road.se',
  [N | W]: 'road.nw',
  [S | W]: 'road.sw',
  [N | S | E]: 'road.nse',
  [N | S | W]: 'road.nsw',
  [N | E | W]: 'road.new',
  [S | E | W]: 'road.sew',
  [N | S | E | W]: 'road.nsew'
}

/** Tope de celdas por trazado: corta trazados degenerados sin colgar el frame. */
const MAX_CELLS = 100_000

/**
 * Celdas atravesadas por el segmento continuo a→b (DDA Amanatides-Woo).
 * Garantiza adyacencia 4-conexa: al cruzar exactamente una esquina emite la
 * celda intermedia en U en lugar de saltar en diagonal.
 */
export function supercoverLine(a: TilePoint, b: TilePoint): GridCell[] {
  let u = Math.floor(a.u)
  let v = Math.floor(a.v)
  const endU = Math.floor(b.u)
  const endV = Math.floor(b.v)
  const du = b.u - a.u
  const dv = b.v - a.v
  const stepU = du > 0 ? 1 : -1
  const stepV = dv > 0 ? 1 : -1

  const tDeltaU = du === 0 ? Infinity : 1 / Math.abs(du)
  const tDeltaV = dv === 0 ? Infinity : 1 / Math.abs(dv)
  let tMaxU = du === 0 ? Infinity : (stepU > 0 ? u + 1 - a.u : a.u - u) / Math.abs(du)
  let tMaxV = dv === 0 ? Infinity : (stepV > 0 ? v + 1 - a.v : a.v - v) / Math.abs(dv)

  const cells: GridCell[] = [{ u, v }]
  while ((u !== endU || v !== endV) && cells.length < MAX_CELLS) {
    if (tMaxU < tMaxV) {
      u += stepU
      tMaxU += tDeltaU
    } else if (tMaxV < tMaxU) {
      v += stepV
      tMaxV += tDeltaV
    } else {
      // Esquina exacta: paso en U y luego en V, emitiendo la celda intermedia.
      u += stepU
      tMaxU += tDeltaU
      cells.push({ u, v })
      v += stepV
      tMaxV += tDeltaV
    }
    cells.push({ u, v })
  }
  return cells
}

function key(c: GridCell): string {
  return `${c.u},${c.v}`
}

function stepDir(from: GridCell, to: GridCell): number {
  if (to.v < from.v) return N
  if (to.v > from.v) return S
  if (to.u > from.u) return E
  return W
}

/**
 * Rasteriza todos los links visibles y resuelve el frame de cada celda con la
 * máscara NSEW global. Devuelve las celdas POR LINK (el renderer las poolea y
 * tinta por congestión por link); celdas compartidas salen en ambos links con
 * el mismo frame de cruce (overdraw idéntico, inocuo).
 */
export function rasterizeLinks(
  links: readonly RasterLink[],
  toTile: (lon: number, lat: number) => TilePoint
): Map<string, RoadCell[]> {
  // 1. Trazado de cada link como secuencia ordenada de celdas 4-conexas.
  const traces = new Map<string, GridCell[]>()
  for (const link of links) {
    const cells: GridCell[] = []
    for (let i = 1; i < link.coords.length; i++) {
      const [lonA, latA] = link.coords[i - 1] as [number, number]
      const [lonB, latB] = link.coords[i] as [number, number]
      for (const c of supercoverLine(toTile(lonA, latA), toTile(lonB, latB))) {
        const last = cells[cells.length - 1]
        if (last !== undefined && last.u === c.u && last.v === c.v) continue
        cells.push(c)
      }
    }
    traces.set(link.id, cells)
  }

  // 2. Máscara global de conexiones por celda (todas las adyacencias de todos
  //    los links cuentan: ahí nacen los cruces y las T entre links).
  const mask = new Map<string, number>()
  for (const cells of traces.values()) {
    for (let i = 1; i < cells.length; i++) {
      const a = cells[i - 1] as GridCell
      const b = cells[i] as GridCell
      const dir = stepDir(a, b)
      mask.set(key(a), (mask.get(key(a)) ?? 0) | dir)
      mask.set(key(b), (mask.get(key(b)) ?? 0) | (OPPOSITE[dir] as number))
    }
  }

  // 3. Frame por celda desde la máscara.
  const out = new Map<string, RoadCell[]>()
  for (const [id, cells] of traces) {
    out.set(
      id,
      cells.map((c) => ({ ...c, frame: FRAME_BY_MASK[mask.get(key(c)) ?? 0] as string }))
    )
  }
  return out
}
