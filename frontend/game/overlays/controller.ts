/**
 * game/overlays/controller — toggles de overlay (FAD §11.9/§16.7).
 *
 * Los overlays son VISIBILIDAD/TINTE de capas o containers, nunca re-render
 * del mundo: alternar uno no re-deriva VMs ni repinta chunks. El ESTADO de los
 * toggles vive en la UI (app/stores/mapui.store) y llega aquí por el mundo
 * vivo (`WorldLive.setOverlay`); este controlador solo lo aplica al render.
 */

export const OVERLAY_NAMES = [
  'logistics',
  'resources',
  'regions',
  'influence',
  'congestion',
] as const

export type OverlayName = (typeof OVERLAY_NAMES)[number]

/**
 * Estado inicial: red y recursos visibles y congestión coloreada (información
 * de juego primaria); regiones e influencia apagadas (analíticas, bajo demanda).
 */
export const DEFAULT_OVERLAYS: Readonly<Record<OverlayName, boolean>> = {
  logistics: true,
  resources: true,
  regions: false,
  influence: false,
  congestion: true,
}

/** Efectos que el controlador aplica (los cablea createWorldLive). */
export interface OverlayTargets {
  /** Capa completa de la escena (red logística: links + nodes; recursos). */
  readonly setLayerVisible: (layer: 'links' | 'resources', visible: boolean) => void
  /** Containers de overlay dentro de la capa overlays. */
  readonly setRegionsVisible: (visible: boolean) => void
  readonly setInfluenceVisible: (visible: boolean) => void
  /** Recoloreado de enlaces por congestión (tinte, no re-render). */
  readonly setCongestionColoring: (on: boolean) => void
}

export class OverlayController {
  private readonly current: Record<OverlayName, boolean> = { ...DEFAULT_OVERLAYS }

  constructor(private readonly targets: OverlayTargets) {
    for (const name of OVERLAY_NAMES) {
      this.applyToTargets(name, this.current[name])
    }
  }

  set(name: OverlayName, on: boolean): void {
    if (this.current[name] === on) {
      return
    }
    this.current[name] = on
    this.applyToTargets(name, on)
  }

  state(): Readonly<Record<OverlayName, boolean>> {
    return { ...this.current }
  }

  private applyToTargets(name: OverlayName, on: boolean): void {
    switch (name) {
      case 'logistics':
        this.targets.setLayerVisible('links', on)
        break
      case 'resources':
        this.targets.setLayerVisible('resources', on)
        break
      case 'regions':
        this.targets.setRegionsVisible(on)
        break
      case 'influence':
        this.targets.setInfluenceVisible(on)
        break
      case 'congestion':
        this.targets.setCongestionColoring(on)
        break
    }
  }
}
