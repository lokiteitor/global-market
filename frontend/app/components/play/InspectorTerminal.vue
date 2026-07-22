<script setup lang="ts">
/**
 * InspectorTerminal — terminal intermodal de un nodo (GDD §7.3, v1.7.0).
 *
 * Capacidad de transbordo, cola actual y SLOTS DE PRIORIDAD (menor tier =
 * antes en la cola; los titulares vigentes pasan antes que el FIFO). Pull
 * bajo demanda por `node.terminalId` (como la demanda de ciudad, C10): la
 * terminal es infraestructura ambiental compartida, no se replica en store.
 * Comprar un slot cobra su precio al dueño de la terminal y fija la
 * titularidad ~30 días de juego; el servidor valida (409 SLOT_HELD,
 * 422 INSUFFICIENT_FUNDS) y la respuesta se aplica a la lista local.
 */

import { computed, ref, watch } from 'vue'
import { t } from '~shared/i18n'
import { format } from '~shared/money'
import type { NodeId, Terminal, TerminalSlot, TerminalSlotId } from '~domain/logistics'
import { isSlotHeld } from '~domain/logistics'
import { isMine } from '~domain/ownership'
import { mapTerminal, mapTerminalSlot } from '~network/mappers/domain.mapper'
import BaseBanner from '~/components/base/BaseBanner.vue'
import BaseButton from '~/components/base/BaseButton.vue'
import BaseSpinner from '~/components/base/BaseSpinner.vue'
import SimTimeCell from '~/components/play/SimTimeCell.vue'
import { useAppError } from '~/composables/useAppError'
import { useGameApis } from '~/composables/useGameApis'
import { useSimNow } from '~/composables/useSimNow'
import { useLogisticsStore } from '~/stores/logistics.store'
import { useSessionStore } from '~/stores/session.store'

interface Props {
  nodeId: NodeId
}

const props = defineProps<Props>()

const apis = useGameApis()
const logistics = useLogisticsStore()
const session = useSessionStore()
const simNow = useSimNow()
const { messageFor } = useAppError()

const terminalId = computed(() => logistics.getNode(props.nodeId)?.terminalId ?? null)

const terminal = ref<Terminal | null>(null)
const slots = ref<readonly TerminalSlot[]>([])
const loading = ref(false)
const fetchError = ref<unknown>(null)

const buyingSlotId = ref<TerminalSlotId | null>(null)
const purchasing = ref(false)
const purchaseError = ref<unknown>(null)
const purchasedText = ref<string | null>(null)

watch(
  terminalId,
  (id) => {
    terminal.value = null
    slots.value = []
    if (id === null) {
      return
    }
    loading.value = true
    fetchError.value = null
    Promise.all([apis.fleet.getTerminal(id), apis.fleet.listTerminalSlots(id)])
      .then(([terminalDto, slotDtos]) => {
        terminal.value = mapTerminal(terminalDto)
        slots.value = slotDtos
          .map(mapTerminalSlot)
          .toSorted((a, b) => a.priorityTier - b.priorityTier)
      })
      .catch((error: unknown) => {
        fetchError.value = error
      })
      .finally(() => {
        loading.value = false
      })
  },
  { immediate: true },
)

const myAccountId = computed(() => session.account?.id ?? null)
const ownTerminal = computed(
  () => terminal.value !== null && isMine(terminal.value.ownerAccountId, myAccountId.value),
)

function isHeld(slot: TerminalSlot): boolean {
  const now = simNow.value
  return now === null ? slot.holderAccountId !== null : isSlotHeld(slot, now)
}

function isMySlot(slot: TerminalSlot): boolean {
  return isHeld(slot) && isMine(slot.holderAccountId, myAccountId.value)
}

/** Comprable: sin titular vigente y la terminal no es mía (el dueño no compra). */
function canBuy(slot: TerminalSlot): boolean {
  return !isHeld(slot) && !ownTerminal.value
}

async function onConfirmPurchase(): Promise<void> {
  const slotId = buyingSlotId.value
  if (slotId === null) {
    return
  }
  purchasing.value = true
  purchaseError.value = null
  try {
    const updated = mapTerminalSlot(await apis.fleet.purchaseTerminalSlot(slotId))
    slots.value = slots.value.map((slot) => (slot.id === updated.id ? updated : slot))
    purchasedText.value = t('inspector.terminal.slot.purchased', { tier: updated.priorityTier })
    buyingSlotId.value = null
  } catch (error) {
    purchaseError.value = error
  } finally {
    purchasing.value = false
  }
}

const buyingSlot = computed(() => slots.value.find((slot) => slot.id === buyingSlotId.value) ?? null)
</script>

<template>
  <section v-if="terminalId !== null" class="terminal o-stack" data-testid="inspector-terminal">
    <h4 class="terminal__subtitle">{{ t('inspector.terminal.title') }}</h4>

    <BaseSpinner v-if="loading" size="sm" />
    <BaseBanner v-else-if="fetchError !== null" variant="error">
      {{ messageFor(fetchError) }}
    </BaseBanner>

    <template v-else-if="terminal !== null">
      <dl class="terminal__facts">
        <div>
          <dt>{{ t('inspector.terminal.capacity') }}</dt>
          <dd class="u-numeric">{{ terminal.transshipmentPerHour }}/h</dd>
        </div>
        <div>
          <dt>{{ t('inspector.terminal.queue') }}</dt>
          <dd class="u-numeric" data-testid="terminal-queue">{{ terminal.queueLength }}</dd>
        </div>
        <div>
          <dt>{{ t('inspector.terminal.owner') }}</dt>
          <dd>{{ ownTerminal ? t('inspector.terminal.owner.own') : t('inspector.terminal.owner.other') }}</dd>
        </div>
      </dl>

      <h5 class="terminal__subtitle">{{ t('inspector.terminal.slots') }}</h5>
      <p class="terminal__muted">{{ t('terminal.slot.validityHint') }}</p>
      <table class="terminal__table">
        <thead>
          <tr>
            <th>{{ t('inspector.terminal.slot.tier') }}</th>
            <th>{{ t('inspector.terminal.slot.price') }}</th>
            <th>{{ t('inspector.terminal.slot.holder') }}</th>
            <th />
          </tr>
        </thead>
        <tbody>
          <tr v-for="slot of slots" :key="slot.id" data-testid="terminal-slot-row">
            <td class="u-numeric">{{ slot.priorityTier }}</td>
            <td class="u-numeric">{{ format(slot.price) }}</td>
            <td>
              <span v-if="isMySlot(slot)" class="terminal__own" data-testid="slot-own">
                {{ t('inspector.terminal.slot.own') }}
              </span>
              <template v-else-if="isHeld(slot)">
                <template v-if="slot.validUntilSim !== null">
                  <SimTimeCell :at="slot.validUntilSim" />
                </template>
                <span v-else>{{ t('inspector.terminal.slot.held') }}</span>
              </template>
              <span v-else class="terminal__muted">{{ t('inspector.terminal.slot.free') }}</span>
            </td>
            <td>
              <BaseButton
                v-if="canBuy(slot)"
                variant="ghost"
                data-testid="slot-buy"
                @click="buyingSlotId = slot.id"
              >
                {{ t('inspector.terminal.slot.buy') }}
              </BaseButton>
            </td>
          </tr>
        </tbody>
      </table>

      <div v-if="buyingSlot !== null" class="terminal__confirm" data-testid="slot-confirm-box">
        <p>
          {{
            t('inspector.terminal.slot.confirmText', {
              tier: buyingSlot.priorityTier,
              price: format(buyingSlot.price),
            })
          }}
        </p>
        <div class="terminal__actions">
          <BaseButton variant="ghost" @click="buyingSlotId = null">
            {{ t('common.cancel') }}
          </BaseButton>
          <BaseButton :loading="purchasing" data-testid="slot-confirm" @click="onConfirmPurchase">
            {{ t('common.confirm') }}
          </BaseButton>
        </div>
      </div>

      <BaseBanner v-if="purchaseError !== null" variant="error">
        {{ messageFor(purchaseError) }}
      </BaseBanner>
      <BaseBanner v-else-if="purchasedText !== null" variant="info">
        {{ purchasedText }}
      </BaseBanner>
    </template>
  </section>
</template>

<style scoped lang="scss">
@use 'settings' as s;

.terminal__subtitle {
  color: var(--color-text-muted);
  font-size: s.$font-size-200;
  font-weight: s.$font-weight-semibold;
  text-transform: uppercase;
}

.terminal__facts {
  display: flex;
  flex-direction: column;
  gap: s.$space-2;

  div {
    display: flex;
    justify-content: space-between;
    gap: s.$space-3;
  }

  dt {
    color: var(--color-text-muted);
  }
}

.terminal__muted {
  color: var(--color-text-muted);
  font-size: s.$font-size-200;
}

.terminal__own {
  color: var(--color-accent);
  font-weight: s.$font-weight-semibold;
}

.terminal__table {
  width: 100%;
  border-collapse: collapse;
  font-size: s.$font-size-200;

  th {
    color: var(--color-text-muted);
    font-weight: s.$font-weight-medium;
    text-align: left;
  }

  th,
  td {
    padding: s.$space-1 s.$space-2;
    border-bottom: 1px solid var(--color-border);
  }
}

.terminal__confirm {
  padding: s.$space-3;
  border: 1px solid var(--color-border);
  border-radius: 0.375rem;
}

.terminal__actions {
  display: flex;
  justify-content: flex-end;
  gap: s.$space-3;
  margin-top: s.$space-3;
}
</style>
