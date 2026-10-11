<template>
  <BaseDialog :show="show" :title="t('common.points.adjust')" width="narrow" :show-close-button="!saving" :close-on-escape="!saving" @close="$emit('close')">
    <form v-if="user" id="points-form" class="space-y-4" @submit.prevent="submit">
      <p>{{ user.email }}</p>
      <p class="font-semibold">{{ t('common.points.label') }}: {{ user.points_balance ?? 0 }}</p>
      <p class="text-sm text-gray-500">{{ t('common.points.debtHint') }}</p>
      <fieldset :disabled="saving" class="space-y-4">
      <div><label class="input-label">{{ t('common.points.operation') }}</label><select v-model="operation" class="input"><option value="add">{{ t('common.points.add') }}</option><option value="subtract">{{ t('common.points.subtract') }}</option><option value="set">{{ t('common.points.set') }}</option></select></div>
      <div><label class="input-label">{{ t('common.points.amount') }}</label><input v-model.number="amount" type="number" min="0" max="999999999999" step="0.00000001" class="input" required /></div>
      <div><label class="input-label">{{ t('common.points.notes') }}</label><textarea v-model="notes" class="input" rows="2" /></div>
      </fieldset>
      <button class="btn btn-secondary" type="button" @click="history = true">{{ t('common.points.history') }}</button>
    </form>
    <template #footer><div class="flex justify-end gap-3"><button class="btn btn-secondary" :disabled="saving" @click="$emit('close')">{{ t('common.cancel') }}</button><button class="btn btn-primary" form="points-form" type="submit" :disabled="saving">{{ t('common.save') }}</button></div></template>
  </BaseDialog>
  <PointsHistoryDialog :show="history && show" :user-id="user?.id" @close="history = false" />
</template>

<script setup lang="ts">
import { ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { updatePoints } from '@/api/admin/users'
import { useAppStore } from '@/stores/app'
import { extractApiErrorMessage } from '@/utils/apiError'
import type { AdminUser } from '@/types'
import BaseDialog from '@/components/common/BaseDialog.vue'
import PointsHistoryDialog from '@/components/user/PointsHistoryDialog.vue'
const props = defineProps<{ show: boolean; user: AdminUser | null }>()
const emit = defineEmits(['close', 'success'])
const { t } = useI18n(), app = useAppStore()
const operation = ref<'add' | 'subtract' | 'set'>('add'), amount = ref(0), notes = ref(''), saving = ref(false), history = ref(false)
let key = ''
let session = 0
watch(() => [props.show, props.user?.id], () => { ++session; operation.value = 'add'; amount.value = 0; notes.value = ''; history.value = false; key = crypto.randomUUID() }, { immediate: true })
watch([operation, amount, notes], () => { key = crypto.randomUUID() })
async function submit() {
  if (!props.user || saving.value || !Number.isFinite(amount.value) || amount.value < 0 || amount.value > 999999999999) return
  const submittedSession = session
  key ||= crypto.randomUUID()
  saving.value = true
  try { await updatePoints(props.user.id, amount.value, operation.value, notes.value, key); app.showSuccess(t('common.success')); emit('success'); if (submittedSession === session) emit('close') }
  catch (e) { app.showError(extractApiErrorMessage(e, t('common.error'))) }
  finally { saving.value = false }
}
</script>
