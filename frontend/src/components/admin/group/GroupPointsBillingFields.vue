<template>
  <div class="space-y-3 border-t pt-4">
    <div class="flex items-center justify-between gap-4">
      <div>
        <label class="input-label">{{ t('common.points.allow') }}</label>
        <p class="input-hint">{{ t('common.points.allowHint') }}</p>
      </div>
      <Toggle :model-value="enabled" @update:model-value="$emit('update:enabled', $event)" />
    </div>
    <div v-if="enabled">
      <label class="input-label">{{ t('common.points.rateLabel') }}</label>
      <input
        :value="rate"
        type="number"
        min="0.00000001"
        max="999999999999"
        step="0.00000001"
        required
        class="input"
        data-testid="group-points-rate"
        @input="$emit('update:rate', Number(($event.target as HTMLInputElement).value))"
      />
      <p class="input-hint">{{ t('common.points.rateHint') }}</p>
      <p v-if="isValidGroupPointsRate(true, rate)" class="input-hint">
        {{ t('common.points.rateExample', { rate, cost: 0.02, points: Number((0.02 * rate).toFixed(8)) }) }}
      </p>
    </div>
  </div>
</template>

<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import Toggle from '@/components/common/Toggle.vue'
import { isValidGroupPointsRate } from '@/utils/points'

defineProps<{ enabled: boolean; rate: number }>()
defineEmits<{ 'update:enabled': [value: boolean]; 'update:rate': [value: number] }>()
const { t } = useI18n()
</script>
