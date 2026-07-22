<script setup lang="ts">
/**
 * MarketPanel — panel MERCADO con pestañas (FAD §15.4 "Panel", v1).
 *
 * Tablón (pull con filtros) | Publicar | Mis publicaciones | Mis contratos |
 * Mis fletes. El diálogo de aceptación se abre desde el tablón (evento
 * `accept`).
 */

import { ref } from 'vue'
import { t } from '~shared/i18n'
import type { MessageKey } from '~shared/i18n'
import type { Publication } from '~domain/market'
import AcceptDialog from '~/components/play/AcceptDialog.vue'
import FloatingPanel from '~/components/play/FloatingPanel.vue'
import MarketBoard from '~/components/play/MarketBoard.vue'
import MyContracts from '~/components/play/MyContracts.vue'
import MarketPrices from '~/components/play/MarketPrices.vue'
import MyFreights from '~/components/play/MyFreights.vue'
import MyPublications from '~/components/play/MyPublications.vue'
import PublishForm from '~/components/play/PublishForm.vue'
import { usePanelsStore } from '~/stores/panels.store'

const panels = usePanelsStore()

type MarketTab = 'board' | 'publish' | 'mine' | 'contracts' | 'freights' | 'prices'

const TABS: readonly { tab: MarketTab; label: MessageKey }[] = [
  { tab: 'board', label: 'market.tab.board' },
  { tab: 'publish', label: 'market.tab.publish' },
  { tab: 'mine', label: 'market.tab.mine' },
  { tab: 'contracts', label: 'market.tab.contracts' },
  { tab: 'freights', label: 'market.tab.freights' },
  { tab: 'prices', label: 'market.tab.prices' },
]

const activeTab = ref<MarketTab>('board')
const accepting = ref<Publication | null>(null)
</script>

<template>
  <FloatingPanel :title="t('panel.market')" width="52rem" @close="panels.close()">
    <nav class="market-panel__tabs">
      <button
        v-for="entry of TABS"
        :key="entry.tab"
        type="button"
        class="market-panel__tab"
        :class="{ 'market-panel__tab--active': activeTab === entry.tab }"
        :data-testid="`market-tab-${entry.tab}`"
        @click="activeTab = entry.tab"
      >
        {{ t(entry.label) }}
      </button>
    </nav>

    <MarketBoard v-if="activeTab === 'board'" @accept="accepting = $event" />
    <PublishForm v-else-if="activeTab === 'publish'" />
    <MyPublications v-else-if="activeTab === 'mine'" />
    <MyContracts v-else-if="activeTab === 'contracts'" />
    <MyFreights v-else-if="activeTab === 'freights'" />
    <MarketPrices v-else />

    <AcceptDialog v-if="accepting !== null" :publication="accepting" @close="accepting = null" />
  </FloatingPanel>
</template>

<style scoped lang="scss">
@use 'settings' as s;
@use 'tools' as t;

.market-panel__tabs {
  display: flex;
  gap: s.$space-2;
  margin-bottom: s.$space-4;
  border-bottom: 1px solid var(--color-border);
}

.market-panel__tab {
  padding: s.$space-2 s.$space-4;
  border: 0;
  border-bottom: 2px solid transparent;
  background: transparent;
  color: var(--color-text-muted);
  font-size: s.$font-size-300;
  cursor: pointer;

  @include t.focus-ring;

  &:hover {
    color: var(--color-text);
  }
}

.market-panel__tab--active {
  border-bottom-color: var(--color-accent);
  color: var(--color-text-strong);
}
</style>
