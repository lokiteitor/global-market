<script setup lang="ts">
/**
 * ConcessionsPanel — mis concesiones de suelo (mandato §3 CONCESIONES).
 *
 * Vencimiento en sim-time con cuenta atrás y renovación (paga el canon del
 * periodo). Las nuevas concesiones se solicitan con la herramienta PARCELA
 * del mapa (arrastre de rectángulo → diálogo de confirmación con canon).
 */

import { computed, ref } from 'vue'
import { t } from '~shared/i18n'
import { format } from '~shared/money'
import type { Concession } from '~domain/cadastre'
import { concessionStatusPresentation } from '~domain/status'
import { mapConcession } from '~network/mappers/domain.mapper'
import BaseBanner from '~/components/base/BaseBanner.vue'
import BaseButton from '~/components/base/BaseButton.vue'
import FloatingPanel from '~/components/play/FloatingPanel.vue'
import SimTimeCell from '~/components/play/SimTimeCell.vue'
import StatusBadge from '~/components/play/StatusBadge.vue'
import { useAppError } from '~/composables/useAppError'
import { useConcessionAlerts } from '~/composables/useConcessionAlerts'
import { useGameApis } from '~/composables/useGameApis'
import { useCadastreStore } from '~/stores/cadastre.store'
import { usePanelsStore } from '~/stores/panels.store'
import { useWorldStore } from '~/stores/world.store'

const apis = useGameApis()
const cadastre = useCadastreStore()
const world = useWorldStore()
const panels = usePanelsStore()
const { messageFor } = useAppError()

const concessions = computed(() => cadastre.concessionList)
const alerts = useConcessionAlerts()

/** Clase de resaltado de fila: danger (impago/gracia) > warn (vence pronto). */
function rowClass(concession: Concession): string | null {
  if (concession.status === 'delinquent' || concession.status === 'grace') {
    return 'concessions__row--danger'
  }
  return alerts.expiringSoon.value.some((c) => c.id === concession.id)
    ? 'concessions__row--warn'
    : null
}

const renewingId = ref<string | null>(null)
const actionError = ref<unknown>(null)

async function onRenew(concession: Concession): Promise<void> {
  renewingId.value = concession.id
  actionError.value = null
  try {
    cadastre.applyConcession(mapConcession(await apis.world.renewConcession(concession.id)))
  } catch (error) {
    actionError.value = error
  } finally {
    renewingId.value = null
  }
}
</script>

<template>
  <FloatingPanel :title="t('panel.concessions')" @close="panels.close()">
    <div class="o-stack">
      <p class="concessions__hint">{{ t('concessions.hint') }}</p>
      <p v-if="alerts.severity.value !== 'none'" class="concessions__warning">
        {{ t('concessions.expiryWarning') }}
      </p>

      <BaseBanner v-if="actionError !== null" variant="error">
        {{ messageFor(actionError) }}
      </BaseBanner>

      <p v-if="concessions.length === 0" class="concessions__empty">
        {{ t('concessions.empty') }}
      </p>

      <table v-else class="concessions__table">
        <thead>
          <tr>
            <th>{{ t('concessions.col.region') }}</th>
            <th>{{ t('concessions.col.status') }}</th>
            <th>{{ t('concessions.col.canon') }}</th>
            <th>{{ t('concessions.col.expires') }}</th>
            <th />
          </tr>
        </thead>
        <tbody>
          <tr v-for="concession of concessions" :key="concession.id" :class="rowClass(concession)">
            <td>{{ world.getRegion(concession.regionId)?.name ?? concession.regionId }}</td>
            <td><StatusBadge :presentation="concessionStatusPresentation(concession.status)" /></td>
            <td class="u-numeric">{{ format(concession.canonAmount) }}</td>
            <td><SimTimeCell :at="concession.expiresAtSim" countdown /></td>
            <td>
              <BaseButton
                v-if="concession.status !== 'reverted'"
                variant="ghost"
                :loading="renewingId === concession.id"
                @click="onRenew(concession)"
              >
                {{ t('concessions.renew') }}
              </BaseButton>
            </td>
          </tr>
        </tbody>
      </table>
    </div>
  </FloatingPanel>
</template>

<style scoped lang="scss">
@use 'settings' as s;

.concessions__hint,
.concessions__empty {
  color: var(--color-text-muted);
  font-size: s.$font-size-200;
}

.concessions__warning {
  color: var(--color-warning);
  font-size: s.$font-size-200;
}

.concessions__table {
  width: 100%;
  border-collapse: collapse;
  font-size: s.$font-size-300;

  th {
    color: var(--color-text-muted);
    font-weight: s.$font-weight-medium;
    text-align: left;
  }

  th,
  td {
    padding: s.$space-2 s.$space-3;
    border-bottom: 1px solid var(--color-border);
  }
}

.concessions__row--warn td:first-child {
  box-shadow: inset 3px 0 0 var(--color-warning);
}

.concessions__row--danger td:first-child {
  box-shadow: inset 3px 0 0 var(--color-danger);
}
</style>
