<!-- Copyright (c) 2026 WorkMesh. 保留所有权利。 -->
<template><AppLayout><div class="space-y-4 p-6"><h1 class="text-xl font-semibold">{{t('speech.bills')}}</h1><p>{{t('speech.billHint')}}</p>
<div v-if="loading" aria-busy="true" class="h-32 animate-pulse rounded bg-gray-100 dark:bg-gray-800"/>
<p v-if="error" role="alert">{{error}} <button :disabled="loading" @click="load">{{t('speech.retry')}}</button></p>
<div v-if="rows.length" class="overflow-x-auto"><table class="w-full min-w-[760px] text-left [&_th]:px-3 [&_th]:py-2 [&_td]:px-3 [&_td]:py-3"><thead><tr><th>{{t('speech.time')}}</th><th>{{t('speech.id')}}</th><th>{{t('speech.kind')}}</th><th>{{t('speech.quantity')}}</th><th>{{t('speech.points')}}</th><th>{{t('speech.state')}}</th></tr></thead>
<tbody><tr v-for="row in rows" :key="row.id"><td>{{new Date(row.createdAt).toLocaleString()}}</td><td>{{row.model}}<small class="block">{{row.priceVersion}}</small></td><td>{{row.feature.toUpperCase()}}</td><td>{{row.quantity}} {{row.feature==='asr'?'s':t('speech.characters')}}</td><td>{{row.points.toFixed(8)}}</td><td>{{row.uncertain?t('speech.uncertain'):t('speech.sessionStates.'+row.state)}} {{row.billingState&&!row.uncertain?t('speech.billingStates.'+row.billingState):!row.uncertain&&row.pending?t('speech.pending'):''}}</td></tr></tbody></table></div>
<p v-else-if="!loading&&!error">{{t('speech.empty')}}</p><button v-if="cursor" class="btn btn-secondary" :disabled="loading" @click="load">{{t('speech.more')}}</button>
</div></AppLayout></template>
<script setup lang="ts">
import {onMounted,ref} from 'vue'
import {useI18n} from 'vue-i18n'
import AppLayout from '@/components/layout/AppLayout.vue'
import {speechAPI,type SpeechBill} from '@/api/speech'
const {t}=useI18n(),rows=ref<SpeechBill[]>([]),cursor=ref(''),loading=ref(false),error=ref('')
async function load(){if(loading.value)return;loading.value=true;error.value='';try{const r=await speechAPI.bills(cursor.value);rows.value.push(...r.items);cursor.value=r.nextCursor}catch{error.value=t('speech.loadFailed')}finally{loading.value=false}}
onMounted(load)
</script>
