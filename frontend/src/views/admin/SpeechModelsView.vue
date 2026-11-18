<!-- Copyright (c) 2026 WorkMesh. 保留所有权利。 -->
<template><AppLayout><div class="space-y-5 p-6">
 <h1 class="text-xl font-semibold">{{t('speech.title')}}</h1><p>{{t('speech.hint')}}</p>
 <div v-if="loading" aria-busy="true" class="h-32 animate-pulse rounded bg-gray-100 dark:bg-gray-800"/>
 <p v-if="error" role="alert" class="text-red-600">{{error}}</p>
 <template v-if="!loading">
 <fieldset :disabled="saving" class="space-y-4">
 <div class="flex flex-wrap items-end gap-3"><label class="min-w-0 flex-1">{{t('speech.accountSearch')}}<input v-model="accountSearch" class="input" maxlength="100"/></label><button class="btn btn-secondary shrink-0 whitespace-nowrap" type="button" :disabled="accountsLoading" @click="loadAccounts(true)">{{t('speech.search')}}</button><button v-if="accountsMore" class="btn btn-secondary shrink-0 whitespace-nowrap" type="button" :disabled="accountsLoading" @click="loadAccounts(false)">{{t('speech.more')}}</button></div>
 <article v-for="(model,index) in models" :key="index" class="grid gap-3 rounded border p-4 md:grid-cols-3">
  <label v-for="field in textFields" :key="field">{{t('speech.'+field)}}<input v-model="model[field]" class="input" :maxlength="100"/></label>
  <label>{{t('speech.provider')}}<select v-model="model.provider" class="input" @change="resetCapability(model)"><option value="aliyun">Alibaba</option><option value="volcengine">ByteDance</option></select></label>
  <label>{{t('speech.kind')}}<select v-model="model.kind" class="input" @change="resetCapability(model)"><option value="asr">ASR</option><option value="tts">TTS</option></select></label>
  <label>{{t('speech.accountId')}}<select v-model.number="model.accountId" class="input"><option :value="0">{{t('speech.selectAccount')}}</option><option v-if="model.accountId&&!accounts.some(a=>a.id===model.accountId)" :value="model.accountId">#{{model.accountId}}</option><option v-for="account in accounts" :key="account.id" :value="account.id">{{account.name}} · #{{account.id}}</option></select></label>
  <label>{{t('speech.price')}}<input v-model.number="model.usdPerUnit" type="number" min="0" step="0.00000001" class="input"/> USD / {{model.unit}}</label>
  <label>{{t('speech.groups')}}<select v-model="model.groups" multiple class="input"><option v-for="group in groups" :key="group.id" :value="group.id">{{group.name}} · #{{group.id}}</option></select></label>
  <label v-if="model.kind==='asr'">{{t('speech.languages')}}<select :value="model.languages[0]||''" class="input" @change="model.languages=[($event.target as HTMLSelectElement).value]"><option v-if="model.provider==='volcengine'" value="auto">{{t('speech.autoLanguage')}}</option><template v-else><option value="zh-CN">中文</option><option value="en-US">English</option></template></select></label>
  <label v-if="model.kind==='tts'">{{t('speech.languages')}}<input :value="model.languages.join(',')" class="input" @change="setList(model,'languages',$event)"/></label>
  <label v-if="model.kind==='tts'">{{t('speech.voices')}}<input :value="model.voices.join(',')" class="input" @change="setList(model,'voices',$event)"/></label>
  <label><input v-model="model.enabled" type="checkbox"/> {{t('speech.enabled')}}</label>
  <p v-if="validation?.index===index" role="alert" class="text-red-600 md:col-span-3">{{t('speech.'+validation.key)}}</p>
  <button class="btn btn-secondary" type="button" @click="models.splice(index,1)">{{t('speech.remove')}}</button>
 </article>
 <label v-if="models.some(m=>m.usdPerUnit===0)"><input v-model="zeroConfirmed" type="checkbox"/> {{t('speech.zeroConfirm')}}</label>
 <div class="flex gap-3"><button class="btn btn-secondary" type="button" :disabled="models.length>=100" @click="add">{{t('speech.add')}}</button><button class="btn btn-primary" type="button" @click="save">{{t('speech.save')}}</button><button class="btn btn-secondary" type="button" @click="reload">{{t('speech.reload')}}</button></div>
 </fieldset><p role="status">{{saved?t('speech.saved'):''}}</p>
 </template>
 <section class="space-y-3 border-t pt-4"><h2 class="text-lg font-semibold">{{t('speech.reconciliation')}}</h2><p>{{t('speech.reconciliationHint')}}</p>
 <p v-if="reconciliationError" role="alert" class="text-red-600">{{reconciliationError}}</p>
 <button class="btn btn-secondary" :disabled="reconciliationLoading||reconciliationSaving" @click="loadReconciliation(true)">{{t('speech.reload')}}</button>
 <article v-for="row in pending" :key="row.id" class="rounded border p-3"><p>{{row.provider}} · {{row.model}} · {{row.id}}</p><p>{{t('speech.providerRequest')}}: {{row.providerRequestId}}</p><p>{{row.quantity}} {{row.unit}}</p><button class="btn btn-secondary" :disabled="reconciliationSaving" @click="selectReconciliation(row)">{{t('speech.review')}}</button></article>
 <button v-if="reconciliationCursor" class="btn btn-secondary" :disabled="reconciliationLoading||reconciliationSaving" @click="loadReconciliation(false)">{{t('speech.more')}}</button>
 <fieldset v-if="review" :disabled="reconciliationSaving" class="space-y-3 rounded border p-4"><p>{{review.id}}</p><label>{{t('speech.decision')}}<select v-model="decision" class="input"><option value="settled">{{t('speech.confirmConsumption')}}</option><option value="released">{{t('speech.confirmNoConsumption')}}</option></select></label><label v-if="decision==='settled'">{{t('speech.quantity')}}<input v-model.number="quantity" type="number" min="0" step="0.00000001" class="input"/></label><label>{{t('speech.evidence')}}<input v-model="evidence" maxlength="200" class="input"/></label><button class="btn btn-primary" @click="reconcile">{{t('speech.confirmReview')}}</button><button class="btn btn-secondary" @click="review=undefined">{{t('speech.cancel')}}</button></fieldset>
 </section>
</div></AppLayout></template>
<script setup lang="ts">
import {computed,onMounted,ref} from 'vue'
import {onBeforeRouteLeave} from 'vue-router'
import {useI18n} from 'vue-i18n'
import AppLayout from '@/components/layout/AppLayout.vue'
import {speechAPI,type SpeechModel,type SpeechReconciliationRow} from '@/api/speech'
import {validateSpeechModels} from '@/api/speech-config'
import {list as accountList} from '@/api/admin/accounts'
import {getAll as groupList} from '@/api/admin/groups'
const {t}=useI18n(),models=ref<SpeechModel[]>([]),previous=ref<SpeechModel[]>([]),version=ref(''),loading=ref(true),saving=ref(false),saved=ref(false),error=ref(''),zeroConfirmed=ref(false)
const validation=ref<{index:number;key:string}>(),accounts=ref<{id:number;name:string}[]>([]),groups=ref<{id:number;name:string}[]>([]),accountSearch=ref(''),accountsLoading=ref(false),accountsMore=ref(false)
let accountPage=0
const textFields=['id','label','version','region','upstreamModel','resourceId'] as const
const dirty=computed(()=>JSON.stringify(models.value)!==JSON.stringify(previous.value))
const pending=ref<SpeechReconciliationRow[]>([]),reconciliationCursor=ref(''),reconciliationLoading=ref(false),reconciliationSaving=ref(false),reconciliationError=ref(''),review=ref<SpeechReconciliationRow>(),decision=ref<'settled'|'released'>('settled'),quantity=ref(0),evidence=ref('')
function setList(model:SpeechModel,field:'languages'|'voices',event:Event){model[field]=[...new Set((event.target as HTMLInputElement).value.split(',').map(s=>s.trim()).filter(Boolean))]}
function resetCapability(m:SpeechModel){m.unit=m.kind==='asr'?'seconds':'characters';m.languages=[m.provider==='volcengine'&&m.kind==='asr'?'auto':'zh-CN'];m.region=m.provider==='aliyun'?(m.kind==='asr'?'cn-shanghai':'cn-beijing'):'';m.voices=[]}
async function loadAccounts(reset:boolean){if(accountsLoading.value)return;accountsLoading.value=true;try{const page=reset?1:accountPage+1;const r=await accountList(page,100,{status:'active',search:accountSearch.value});accounts.value=reset?r.items:[...accounts.value,...r.items];accountPage=page;accountsMore.value=page*100<r.total}catch{error.value=t('speech.loadFailed')}finally{accountsLoading.value=false}}
async function load(){loading.value=true;error.value='';validation.value=undefined;saved.value=false;try{const [r,g]=await Promise.all([speechAPI.config(),groupList()]);models.value=r.items;previous.value=structuredClone(r.items);version.value=r.version;groups.value=g.filter(v=>v.allow_points_billing);zeroConfirmed.value=false}catch{error.value=t('speech.loadFailed')}finally{loading.value=false}}
function reload(){if(!dirty.value||window.confirm(t('speech.unsaved')))void load()}
function add(){models.value.push({id:'',label:'',version:'',kind:'asr',provider:'aliyun',accountId:0,region:'cn-shanghai',upstreamModel:'',resourceId:'',groups:[],languages:['zh-CN'],voices:[],usdPerUnit:null,unit:'seconds',enabled:false})}
async function save(){if(saving.value)return;validation.value=validateSpeechModels(models.value,previous.value);saved.value=false;error.value='';if(validation.value)return;if(models.value.some(m=>m.usdPerUnit===0)&&!zeroConfirmed.value){error.value=t('speech.zeroConfirm');return}
 saving.value=true;try{const snapshot=JSON.parse(JSON.stringify(models.value)) as SpeechModel[];const r=await speechAPI.save(snapshot,version.value);version.value=r.version;previous.value=snapshot;saved.value=true}catch(e){const code=(e as {message?:string}).message;const messages:Record<string,string>={SPEECH_CONFIG_CONFLICT:'configConflict',SPEECH_PRICE_VERSION_REQUIRED:'configVersion',SPEECH_ACCOUNT_INVALID:'configAccount',SPEECH_GROUP_INVALID:'configGroups'};error.value=t('speech.'+(messages[code||'']||'saveFailed'))}finally{saving.value=false}}
async function loadReconciliation(reset:boolean){if(reconciliationLoading.value)return;reconciliationLoading.value=true;reconciliationError.value='';try{const r=await speechAPI.reconciliation(reset?'':reconciliationCursor.value);pending.value=reset?r.items:[...pending.value,...r.items];reconciliationCursor.value=r.nextCursor}catch{reconciliationError.value=t('speech.loadFailed')}finally{reconciliationLoading.value=false}}
function selectReconciliation(row:SpeechReconciliationRow){review.value=row;decision.value='settled';quantity.value=row.quantity;evidence.value=''}
async function reconcile(){if(!review.value||reconciliationSaving.value)return;const units=decision.value==='released'?0:quantity.value;if(!evidence.value.trim()||!Number.isFinite(units)||decision.value==='settled'&&units<=0){reconciliationError.value=t('speech.reviewInvalid');return};reconciliationSaving.value=true;reconciliationError.value='';try{await speechAPI.reconcile(review.value.id,{decision:decision.value,quantity:units,evidence:evidence.value.trim()});review.value=undefined;await loadReconciliation(true)}catch{reconciliationError.value=t('speech.reviewFailed')}finally{reconciliationSaving.value=false}}
onBeforeRouteLeave(()=>!saving.value&&!reconciliationSaving.value&&(!dirty.value||window.confirm(t('speech.unsaved'))))
onMounted(()=>{void load();void loadAccounts(true);void loadReconciliation(true)})
</script>
