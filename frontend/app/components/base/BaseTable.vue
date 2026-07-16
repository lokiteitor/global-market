<!--
  BaseTable — tabla tipada del UI kit propio.
  - Columnas declarativas (key/label/align/width).
  - Slot de celda por columna: #cell-<key>="{ row }".
  - Virtualización simple por scroll cuando hay > VIRTUAL_THRESHOLD filas:
    ventana [start, end] con espaciadores de altura fija (rowHeight).
-->
<script setup lang="ts" generic="T">
import { computed, ref } from 'vue'

export interface TableColumn {
  key: string
  label: string
  align?: 'left' | 'right' | 'center'
  width?: string
}

const VIRTUAL_THRESHOLD = 200
const OVERSCAN = 10

const props = withDefaults(
  defineProps<{
    columns: TableColumn[]
    rows: T[]
    rowKey: (row: T) => string
    /** Alto fijo de fila (px) — requisito de la virtualización simple. */
    rowHeight?: number
    /** Alto máximo del cuerpo con scroll. */
    maxHeight?: string
    emptyText?: string
  }>(),
  { rowHeight: 32, maxHeight: '24rem', emptyText: 'Sin resultados' }
)

const emit = defineEmits<{ 'row-click': [row: T] }>()

const scrollTop = ref(0)
const viewportEl = ref<HTMLElement | null>(null)

const virtual = computed(() => props.rows.length > VIRTUAL_THRESHOLD)

const range = computed(() => {
  if (!virtual.value) return { start: 0, end: props.rows.length }
  const viewportPx = viewportEl.value?.clientHeight ?? 400
  const visible = Math.ceil(viewportPx / props.rowHeight)
  const start = Math.max(0, Math.floor(scrollTop.value / props.rowHeight) - OVERSCAN)
  const end = Math.min(props.rows.length, start + visible + OVERSCAN * 2)
  return { start, end }
})

const visibleRows = computed(() => props.rows.slice(range.value.start, range.value.end))
const topPad = computed(() => range.value.start * props.rowHeight)
const bottomPad = computed(() => (props.rows.length - range.value.end) * props.rowHeight)

function onScroll(event: Event): void {
  scrollTop.value = (event.target as HTMLElement).scrollTop
}

function cellValue(row: T, key: string): string {
  const value = (row as Record<string, unknown>)[key]
  if (value === undefined || value === null) return '—'
  return String(value)
}
</script>

<template>
  <div ref="viewportEl" class="b-table" :style="{ maxHeight }" @scroll.passive="onScroll">
    <table class="b-table__table" :aria-rowcount="rows.length">
      <thead>
        <tr>
          <th
            v-for="col in columns"
            :key="col.key"
            scope="col"
            :style="{ width: col.width, textAlign: col.align ?? 'left' }"
          >
            {{ col.label }}
          </th>
        </tr>
      </thead>
      <tbody>
        <tr v-if="virtual && topPad > 0" aria-hidden="true">
          <td :colspan="columns.length" :style="{ height: `${topPad}px`, padding: 0, border: 'none' }" />
        </tr>
        <tr
          v-for="row in visibleRows"
          :key="rowKey(row)"
          class="b-table__row"
          :style="virtual ? { height: `${rowHeight}px` } : undefined"
          @click="emit('row-click', row)"
        >
          <td v-for="col in columns" :key="col.key" :style="{ textAlign: col.align ?? 'left' }">
            <slot :name="`cell-${col.key}`" :row="row">{{ cellValue(row, col.key) }}</slot>
          </td>
        </tr>
        <tr v-if="virtual && bottomPad > 0" aria-hidden="true">
          <td :colspan="columns.length" :style="{ height: `${bottomPad}px`, padding: 0, border: 'none' }" />
        </tr>
        <tr v-if="rows.length === 0">
          <td :colspan="columns.length" class="b-table__empty">{{ emptyText }}</td>
        </tr>
      </tbody>
    </table>
  </div>
</template>

<style lang="scss" scoped>
.b-table {
  overflow: auto;
  border: 1px solid var(--ii-border-subtle);
  border-radius: 3px;

  &__table {
    width: 100%;
    font-size: 0.875rem;

    th {
      position: sticky;
      top: 0;
      background-color: var(--ii-bg-overlay);
      color: var(--ii-text-muted);
      font-weight: 600;
      font-size: 0.75rem;
      text-transform: uppercase;
      letter-spacing: 0.04em;
      padding: 0.375rem 0.5rem;
      border-bottom: 1px solid var(--ii-border);
      white-space: nowrap;
    }

    td {
      padding: 0.25rem 0.5rem;
      border-bottom: 1px solid var(--ii-border-subtle);
      vertical-align: middle;
    }
  }

  &__row:hover {
    background-color: color-mix(in srgb, var(--ii-accent) 6%, transparent);
  }

  &__empty {
    color: var(--ii-text-faint);
    text-align: center;
    padding-block: 1rem;
  }
}
</style>
