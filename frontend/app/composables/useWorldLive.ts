/**
 * app/composables/useWorldLive — puentes Vue ↔ mundo vivo (FAD §11.6, §11.1).
 *
 * Dos piezas que el host de /play (fase UI) usa tras crear el juego con
 * `const mod = await import('~~/game')`:
 *
 * - `createWorldStateSource()`: implementa el puerto `WorldStateSource` del
 *   bridge sobre las stores Pinia (world, buildings, logistics, fleet,
 *   cadastre, session) + el SimClock único. Los cambios de CUALQUIERA de esas
 *   stores (estado replicado: solo escriben respuestas/eventos del servidor)
 *   notifican al bridge, que coalesce a ≤1 recomputación por frame.
 *
 * - `bindWorldLive(live)`: sincronización BIDIRECCIONAL con la store de UI del
 *   mapa (mapui.store): la UI escribe modo/overlays/selección/follow en la
 *   store y el binder los aplica al motor; los eventos del motor (picking,
 *   follow cancelado por pan…) se reflejan de vuelta. Con guardas de igualdad
 *   para no ciclar.
 *
 * SOLO tipos del motor se importan estáticamente (erasados al compilar): el
 * bundle de Phaser sigue siendo de carga perezosa exclusiva de /play (O7).
 */

import { watch } from 'vue'
import type { SimClock } from '~domain/simclock'
import type { OverlayName, WorldLive, WorldStateSource } from '~~/game'
import { useBuildingsStore } from '~/stores/buildings.store'
import { useCadastreStore } from '~/stores/cadastre.store'
import { useFleetStore } from '~/stores/fleet.store'
import { useLogisticsStore } from '~/stores/logistics.store'
import { useMapUiStore } from '~/stores/mapui.store'
import { useSessionStore } from '~/stores/session.store'
import { useWorldStore } from '~/stores/world.store'

export interface WorldStateSourceHandle {
  readonly source: WorldStateSource
  /** Detiene el watcher de stores (al destruir el juego). */
  readonly dispose: () => void
}

/**
 * Implementación Pinia del puerto `WorldStateSource`. Llamar dentro de un
 * contexto con Pinia activa (setup del host de /play).
 */
export function createWorldStateSource(): WorldStateSourceHandle {
  const world = useWorldStore()
  const buildings = useBuildingsStore()
  const logistics = useLogisticsStore()
  const fleet = useFleetStore()
  const cadastre = useCadastreStore()
  const session = useSessionStore()
  const { $simClock } = useNuxtApp() as { $simClock?: SimClock }

  const listeners = new Set<() => void>()

  // Un único watcher sobre los subárboles replicados relevantes: cualquier
  // apply*/remove* de esas stores reemplaza su Record inmutable y dispara.
  const stopWatch = watch(
    [
      () => world.regionById,
      () => world.cityById,
      () => world.depositById,
      () => world.buildingTypeById,
      () => buildings.buildingById,
      () => logistics.nodeById,
      () => logistics.linkById,
      () => fleet.vehicleById,
      () => cadastre.concessionById,
      () => session.account,
    ],
    () => {
      for (const listener of listeners) {
        listener()
      }
    },
  )

  const source: WorldStateSource = {
    regions: () => world.regionList,
    cities: () => world.cityList,
    deposits: () => world.depositList,
    nodes: () => logistics.nodeList,
    links: () => logistics.linkList,
    buildings: () => buildings.buildingList,
    vehicles: () => fleet.vehicleList,
    concessions: () => cadastre.concessionList,
    buildingTypeCode: (id) => world.getBuildingType(id)?.code ?? null,
    segmentContext: (segmentId) => logistics.segmentContext(segmentId),
    ownAccountId: () => session.account?.id ?? null,
    simNow: () => $simClock?.now() ?? null,
    subscribe: (listener) => {
      listeners.add(listener)
      return () => {
        listeners.delete(listener)
      }
    },
  }

  return {
    source,
    dispose: () => {
      stopWatch()
      listeners.clear()
    },
  }
}

/**
 * Sincroniza `WorldLive` ⇄ mapui.store. Devuelve la función de desmontaje
 * (llamarla antes de destruir el juego).
 */
export function bindWorldLive(live: WorldLive): () => void {
  const ui = useMapUiStore()

  // Estado inicial: el motor manda en overlays (defaults); la UI, en el modo.
  ui.applyOverlays(live.overlays())
  live.setMode(ui.mode)
  ui.setSelection(live.selection())
  ui.setFollow(live.followedVehicleId())

  const stops = [
    watch(
      () => ui.mode,
      (mode) => {
        if (live.mode() !== mode) {
          live.setMode(mode)
        }
      },
    ),
    watch(
      () => ui.overlays,
      (overlays) => {
        const current = live.overlays()
        for (const [name, on] of Object.entries(overlays) as [OverlayName, boolean][]) {
          if (current[name] !== on) {
            live.setOverlay(name, on)
          }
        }
      },
    ),
    watch(
      () => ui.selection,
      (selection) => {
        const current = live.selection()
        if (selection?.type !== current?.type || selection?.id !== current?.id) {
          live.select(selection)
        }
      },
    ),
    watch(
      () => ui.followedVehicleId,
      (vehicleId) => {
        if (live.followedVehicleId() !== vehicleId) {
          live.setFollow(vehicleId)
        }
      },
    ),
    // Comandos de cámara (one-shot con seq): la UI pide, el motor ejecuta.
    watch(
      () => ui.cameraCommand,
      (command) => {
        if (command === null) {
          return
        }
        if (command.kind === 'center') {
          live.centerOnM(command.xM, command.yM)
        } else {
          live.fitRectM(command.rectM)
        }
      },
    ),
  ]

  const offs = [
    live.on('mode', (mode) => {
      ui.setMode(mode)
    }),
    live.on('camera', (view) => {
      ui.setCameraView(view.viewM)
    }),
    live.on('selection', (selection) => {
      ui.setSelection(selection)
    }),
    live.on('follow', (vehicleId) => {
      ui.setFollow(vehicleId)
    }),
  ]

  return () => {
    for (const stop of stops) {
      stop()
    }
    for (const off of offs) {
      off()
    }
  }
}
