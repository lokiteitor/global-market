/**
 * build-game-assets.mjs — empaqueta los frames del pak128 (fuente Simutrans)
 * en un atlas Phaser consumible en runtime.
 *
 * Uso: npm run build:assets  (determinista; el output se commitea)
 *
 * Entrada:  app/assets/pak128/**  (tilesheets RGB con celdas de 128×128 y
 *           color clave RGB(231,255,255) como transparencia)
 * Salida:   public/game/pak128/{atlas.png, atlas.json, meta.json, ATTRIBUTION.txt}
 *
 * La lista de frames es declarativa y curada a mano (JOBS): no se parsean los
 * .dat de Simutrans de forma genérica. Las coordenadas fila.columna de cada
 * celda están verificadas contra el .dat correspondiente (ver comentarios).
 *
 * Convenciones del arte pak128 (verificadas escaneando píxeles no-clave):
 *  - Ways/edificios/vehículos: el rombo del tile ocupa la mitad INFERIOR de la
 *    celda 128×128; su centro está en (64, 96) → anchor (0.5, 0.75).
 *  - Las texturas de clima llenan la celda entera (el motor de Simutrans las
 *    recorta a rombo): aquí se recorta un rombo de 128×64 en el empaquetado.
 */
import { PNG } from 'pngjs'
import { readFileSync, writeFileSync, mkdirSync } from 'node:fs'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

const SCRIPTS_DIR = path.dirname(fileURLToPath(import.meta.url))
const PAK_DIR = path.join(SCRIPTS_DIR, '..', 'app', 'assets', 'pak128')
const OUT_DIR = path.join(SCRIPTS_DIR, '..', 'public', 'game', 'pak128')

const CELL = 128
const KEY = { r: 231, g: 255, b: 255 }
/** Anchor estándar: centro del rombo (64, 96) de una celda 128×128. */
const CELL_ANCHOR = { anchorX: 0.5, anchorY: 0.75 }

// ─── Lista curada de frames ──────────────────────────────────────────────────

/** Suelo: landscape/grounds/texture-climate.dat → Image[clima][0]=fila.col */
const GROUNDS = [
  ['ground.water', 0, 0],
  ['ground.desert', 0, 1],
  ['ground.tropic', 0, 2],
  ['ground.grass', 0, 4],
  ['ground.rocky', 0, 6],
  ['ground.snow', 0, 7]
]

/** Carreteras: infrastructure/roads/road_050.dat → Image[máscara NSEW][verano] */
const ROADS = [
  ['road.dead', 1, 0],
  ['road.n', 1, 1],
  ['road.s', 1, 2],
  ['road.e', 1, 3],
  ['road.w', 1, 4],
  ['road.ns', 1, 5],
  ['road.ew', 1, 6],
  ['road.nse', 1, 7],
  ['road.nsw', 2, 0],
  ['road.new', 2, 1],
  ['road.sew', 2, 2],
  ['road.nsew', 2, 3],
  ['road.ne', 2, 4],
  ['road.se', 2, 5],
  ['road.nw', 2, 6],
  ['road.sw', 2, 7]
]

/** Casas 1×1 (frame de verano = backimage[...][0] → fila 1, col 0). */
const HOUSES = [
  ['house.a', 'cityhouses/res/res_00_11.png'],
  ['house.b', 'cityhouses/res/res_00_08.png'],
  ['house.c', 'cityhouses/res/res_00_14.png']
]

/**
 * Camión: vehicles/road-cargo/bulk_truck_0.dat → emptyimage[dir]=fila 1,
 * columnas 0..7 en orden w,nw,n,ne,e,se,s,sw.
 */
const TRUCK_DIRS = ['w', 'nw', 'n', 'ne', 'e', 'se', 's', 'sw']

/**
 * Edificios multi-tile compuestos offline en un único frame.
 * El .dat referencia BackImage[0][x][y]=archivo.(y).(dimsX-1-x): el eje x del
 * juego va INVERTIDO respecto a las columnas de la hoja.
 */
const COMPOSED = [
  ['building.bakery', 'factories/bakery.png', 3, 3],
  ['building.mine', 'factories/erzbergwerk.png', 5, 5]
]

// ─── Utilidades de píxeles (RGBA plano) ──────────────────────────────────────

function loadPng(relPath) {
  return PNG.sync.read(readFileSync(path.join(PAK_DIR, relPath)))
}

/** Copia la celda (row, col) de una hoja a un buffer RGBA 128×128 con color-key → alpha 0. */
function extractCell(sheet, row, col) {
  const out = Buffer.alloc(CELL * CELL * 4)
  for (let y = 0; y < CELL; y++) {
    for (let x = 0; x < CELL; x++) {
      const src = ((row * CELL + y) * sheet.width + (col * CELL + x)) * 4
      const dst = (y * CELL + x) * 4
      const r = sheet.data[src]
      const g = sheet.data[src + 1]
      const b = sheet.data[src + 2]
      const transparent = r === KEY.r && g === KEY.g && b === KEY.b
      out[dst] = r
      out[dst + 1] = g
      out[dst + 2] = b
      out[dst + 3] = transparent ? 0 : 255
    }
  }
  return { data: out, w: CELL, h: CELL }
}

/**
 * Recorta un rombo 128×64 de una textura de clima (que llena la celda entera).
 * Se toma la banda vertical [32, 96) de la celda y se enmascara al rombo.
 */
function extractGroundDiamond(sheet, row, col) {
  const w = 128
  const h = 64
  const out = Buffer.alloc(w * h * 4)
  // Epsilon de solape: sin él, los píxeles frontera no caen en ningún rombo
  // y al tesela r el terreno queda una rejilla oscura entre celdas (visible
  // sobre todo con zoom-out + antialiasing).
  const EPS = 1 / 24
  for (let y = 0; y < h; y++) {
    for (let x = 0; x < w; x++) {
      const inside = Math.abs(x + 0.5 - 64) / 64 + Math.abs(y + 0.5 - 32) / 32 <= 1 + EPS
      const src = ((row * CELL + y + 32) * sheet.width + (col * CELL + x)) * 4
      const dst = (y * w + x) * 4
      out[dst] = sheet.data[src]
      out[dst + 1] = sheet.data[src + 1]
      out[dst + 2] = sheet.data[src + 2]
      out[dst + 3] = inside ? 255 : 0
    }
  }
  return { data: out, w, h }
}

/** Blit con alpha binario (los frames pak128 no tienen semitransparencia). */
function blit(dst, dstW, src, srcW, srcH, offX, offY) {
  for (let y = 0; y < srcH; y++) {
    for (let x = 0; x < srcW; x++) {
      const s = (y * srcW + x) * 4
      if (src[s + 3] === 0) continue
      const d = ((offY + y) * dstW + offX + x) * 4
      dst[d] = src[s]
      dst[d + 1] = src[s + 1]
      dst[d + 2] = src[s + 2]
      dst[d + 3] = 255
    }
  }
}

/**
 * Compone un edificio n×m (tiles de juego) en un frame único con offsets iso.
 * Celda de hoja para el tile (x, y): fila = y, columna = n-1-x (eje invertido).
 * Orden de pintado por x+y ascendente (los tiles del frente tapan a los del fondo).
 */
function composeBuilding(sheet, n, m) {
  const w = (n + m) * 64
  const h = (n + m - 2) * 32 + CELL
  const out = Buffer.alloc(w * h * 4)
  for (let s = 0; s <= n + m - 2; s++) {
    for (let y = 0; y < m; y++) {
      const x = s - y
      if (x < 0 || x >= n) continue
      const cell = extractCell(sheet, y, n - 1 - x)
      blit(out, w, cell.data, CELL, CELL, (x - y + (m - 1)) * 64, (x + y) * 32)
    }
  }
  // Centro del rombo de la huella completa: (w/2, (n+m)*16 + 64).
  return { data: out, w, h, anchorX: 0.5, anchorY: ((n + m) * 16 + 64) / h }
}

// ─── Construcción de frames ──────────────────────────────────────────────────

function buildFrames() {
  const frames = []

  const climate = loadPng('landscape/grounds/texture-climate.png')
  for (const [name, row, col] of GROUNDS) {
    frames.push({ name, ...extractGroundDiamond(climate, row, col), anchorX: 0.5, anchorY: 0.5 })
  }

  const roads = loadPng('infrastructure/roads/road_050.png')
  for (const [name, row, col] of ROADS) {
    frames.push({ name, ...extractCell(roads, row, col), ...CELL_ANCHOR })
  }

  for (const [name, rel] of HOUSES) {
    frames.push({ name, ...extractCell(loadPng(rel), 1, 0), ...CELL_ANCHOR })
  }

  const truck = loadPng('vehicles/road-cargo/bulk_truck_0.png')
  TRUCK_DIRS.forEach((dir, col) => {
    frames.push({ name: `truck.${dir}`, ...extractCell(truck, 1, col), ...CELL_ANCHOR })
  })

  for (const [name, rel, n, m] of COMPOSED) {
    frames.push({ name, ...composeBuilding(loadPng(rel), n, m) })
  }

  return frames
}

// ─── Shelf packing (determinista: alto desc, luego nombre) ───────────────────

const ATLAS_WIDTH = 1024
const PAD = 2

function packFrames(frames) {
  const order = [...frames].sort((a, b) => b.h - a.h || a.name.localeCompare(b.name))
  let cursorX = 0
  let cursorY = 0
  let shelfH = 0
  for (const f of order) {
    if (cursorX + f.w + PAD > ATLAS_WIDTH) {
      cursorX = 0
      cursorY += shelfH + PAD
      shelfH = 0
    }
    f.atlasX = cursorX
    f.atlasY = cursorY
    cursorX += f.w + PAD
    shelfH = Math.max(shelfH, f.h)
  }
  return { frames: order, height: cursorY + shelfH }
}

// ─── Main ────────────────────────────────────────────────────────────────────

const frames = buildFrames()
const { frames: packed, height } = packFrames(frames)

const atlas = new PNG({ width: ATLAS_WIDTH, height })
for (const f of packed) {
  blit(atlas.data, ATLAS_WIDTH, f.data, f.w, f.h, f.atlasX, f.atlasY)
}

mkdirSync(OUT_DIR, { recursive: true })
writeFileSync(path.join(OUT_DIR, 'atlas.png'), PNG.sync.write(atlas))

const atlasJson = {
  frames: Object.fromEntries(
    packed.map((f) => [
      f.name,
      {
        frame: { x: f.atlasX, y: f.atlasY, w: f.w, h: f.h },
        rotated: false,
        trimmed: false,
        sourceSize: { w: f.w, h: f.h },
        spriteSourceSize: { x: 0, y: 0, w: f.w, h: f.h }
      }
    ])
  ),
  meta: {
    app: 'build-game-assets.mjs',
    image: 'atlas.png',
    format: 'RGBA8888',
    size: { w: ATLAS_WIDTH, h: height },
    scale: 1
  }
}
writeFileSync(path.join(OUT_DIR, 'atlas.json'), JSON.stringify(atlasJson, null, 2) + '\n')

const metaJson = {
  version: 1,
  tile: { width: 128, height: 64 },
  frames: Object.fromEntries(
    packed.map((f) => [f.name, { anchorX: f.anchorX, anchorY: f.anchorY }])
  )
}
writeFileSync(path.join(OUT_DIR, 'meta.json'), JSON.stringify(metaJson, null, 2) + '\n')

writeFileSync(
  path.join(OUT_DIR, 'ATTRIBUTION.txt'),
  `Los frames de atlas.png proceden del pakset pak128 de Simutrans
(https://github.com/simutrans/pak128), redistribuidos bajo Artistic License 2.0.
Fuente en este repositorio: frontend/app/assets/pak128/ (LICENSE.txt).
Autores originales: equipo pak128 (ver copyright en los .dat correspondientes,
p. ej. bakery: Patrick; bulk_truck_0: Karl & Shorty).
Generado por frontend/scripts/build-game-assets.mjs (npm run build:assets).
`
)

console.log(`OK: ${packed.length} frames → ${path.relative(process.cwd(), OUT_DIR)} (atlas ${ATLAS_WIDTH}×${height})`)
