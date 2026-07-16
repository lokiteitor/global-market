/**
 * stores/ui.store.ts — bounded context: estado efímero de la UI.
 *
 * Panel activo, selección actual y layout del HUD. Es la única store SIN
 * estado replicado del servidor: puede escribirse libremente desde la UI y
 * desde los intents del event bus ('world:select', 'ui:openPanel').
 */
import { defineStore } from 'pinia'
import { computed, ref } from 'vue'

export type PanelName = 'market' | 'industry' | 'fleet' | 'logistics' | 'city' | 'concessions' | 'finance' | 'notifications' | 'settings'

/**
 * Tipos de entidad seleccionable en el inspector. Los cinco primeros son
 * espaciales (llegan por 'world:select' desde el canvas); 'publication' y
 * 'contract' se seleccionan desde listas de la UI (MarketPanel → inspector).
 */
export type SelectionKind = 'city' | 'building' | 'vehicle' | 'deposit' | 'node' | 'publication' | 'contract'

export interface Selection {
  kind: SelectionKind
  id: string
}

export interface UiLayout {
  sidebarOpen: boolean
  inspectorOpen: boolean
  bottomBarOpen: boolean
}

export const useUiStore = defineStore('ui', () => {
  // ── Estado ──
  const activePanel = ref<PanelName | null>(null)
  const selection = ref<Selection | null>(null)
  const layout = ref<UiLayout>({ sidebarOpen: true, inspectorOpen: true, bottomBarOpen: true })

  // ── Getters ──
  const hasSelection = computed(() => selection.value !== null)
  const isPanelOpen = computed(() => activePanel.value !== null)

  // ── Acciones ──
  function openPanel(panel: PanelName): void {
    activePanel.value = panel
  }

  function closePanel(): void {
    activePanel.value = null
  }

  function togglePanel(panel: PanelName): void {
    activePanel.value = activePanel.value === panel ? null : panel
  }

  function select(kind: SelectionKind, id: string): void {
    selection.value = { kind, id }
  }

  function clearSelection(): void {
    selection.value = null
  }

  function toggleSidebar(): void {
    layout.value.sidebarOpen = !layout.value.sidebarOpen
  }

  function toggleInspector(): void {
    layout.value.inspectorOpen = !layout.value.inspectorOpen
  }

  function toggleBottomBar(): void {
    layout.value.bottomBarOpen = !layout.value.bottomBarOpen
  }

  return {
    activePanel,
    selection,
    layout,
    hasSelection,
    isPanelOpen,
    openPanel,
    closePanel,
    togglePanel,
    select,
    clearSelection,
    toggleSidebar,
    toggleInspector,
    toggleBottomBar
  }
})
