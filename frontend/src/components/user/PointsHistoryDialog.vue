<template>
  <BaseDialog :show="show" :title="t('common.points.history')" width="wide" :z-index="60" @close="$emit('close')">
    <p v-if="error" class="text-sm text-red-600">{{ error }}</p>
    <p v-if="loading" class="text-sm text-gray-500">{{ t('common.loading') }}</p>
    <div v-else class="overflow-x-auto">
      <table class="w-full text-left text-sm">
        <thead><tr class="border-b"><th class="p-2">{{ t('common.points.date') }}</th><th class="p-2">{{ t('common.points.operation') }}</th><th class="p-2">{{ t('common.points.amount') }}</th><th class="p-2">{{ t('common.points.balanceAfter') }}</th><th class="p-2">{{ t('common.points.usd') }}</th><th class="p-2">{{ t('common.points.notes') }}</th></tr></thead>
        <tbody><tr v-for="entry in entries" :key="entry.id" class="border-b border-gray-100 dark:border-dark-700"><td class="p-2 whitespace-nowrap">{{ formatDateTime(entry.created_at) }}</td><td class="p-2">{{ t(`common.points.${entry.kind}`) }}</td><td class="p-2 tabular-nums">{{ entry.amount > 0 ? '+' : '' }}{{ entry.amount }}</td><td class="p-2 tabular-nums">{{ entry.balance_after }}</td><td class="p-2" :title="`1 USD = ${entry.points_per_usd}`">{{ entry.cost_usd }}</td><td class="p-2">{{ entry.notes || entry.request_id || '—' }}</td></tr></tbody>
      </table>
      <p v-if="!entries.length" class="py-6 text-center text-gray-500">{{ t('common.points.empty') }}</p>
    </div>
    <template #footer><div class="flex items-center justify-between gap-3"><button class="btn btn-secondary" :disabled="page <= 1 || loading" @click="load(page - 1)">{{ t('common.points.previous') }}</button><span>{{ page }} / {{ Math.max(1, Math.ceil(total / 20)) }}</span><button class="btn btn-secondary" :disabled="page * 20 >= total || loading" @click="load(page + 1)">{{ t('common.points.next') }}</button></div></template>
  </BaseDialog>
</template>

<script setup lang="ts">
import { ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { apiClient } from '@/api/client'
import BaseDialog from '@/components/common/BaseDialog.vue'
import { formatDateTime } from '@/utils/format'
import { extractApiErrorMessage } from '@/utils/apiError'

const props = defineProps<{ show: boolean; userId?: number }>()
defineEmits(['close'])
const { t } = useI18n()
interface Entry { id: number; amount: number; balance_after: number; cost_usd: number; points_per_usd: number; kind: string; notes: string; request_id: string | null; created_at: string }
const entries = ref<Entry[]>([])
const page = ref(1), total = ref(0), loading = ref(false), error = ref('')
let sequence = 0
async function load(nextPage = 1) {
  const current = ++sequence
  loading.value = true; error.value = ''
  try {
    const path = props.userId ? `/admin/users/${props.userId}/points-history` : '/user/points-history'
    const { data } = await apiClient.get<{ items: Entry[]; total: number }>(path, { params: { page: nextPage, page_size: 20 } })
    if (current !== sequence) return
    entries.value = data.items; total.value = data.total; page.value = nextPage
  } catch (e) { if (current === sequence) error.value = extractApiErrorMessage(e, t('common.error')) }
  finally { if (current === sequence) loading.value = false }
}
watch(() => [props.show, props.userId], () => {
  ++sequence
  entries.value = []; total.value = 0; page.value = 1; error.value = ''
  if (props.show) void load()
}, { immediate: true })
</script>
