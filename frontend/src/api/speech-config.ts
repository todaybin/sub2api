// Copyright (c) 2026 WorkMesh. 保留所有权利。
import type {SpeechModel} from './speech'
export function validateSpeechModels(models:SpeechModel[],previous:SpeechModel[]):{index:number;key:string}|undefined{
 const ids=new Set<string>()
 for(const [index,m]of models.entries()){
  const fail=(key:string)=>({index,key})
  if(!m.id.trim()||m.id.length>100||ids.has(m.id)||!m.label.trim()||m.label.length>100||!m.version.trim()||m.version.length>100||!Number.isSafeInteger(m.accountId)||m.accountId<=0)return fail('configRequired')
  ids.add(m.id)
  if(m.usdPerUnit===null||typeof m.usdPerUnit!=='number'||!Number.isFinite(m.usdPerUnit)||m.usdPerUnit<0)return fail('configPrice')
  if(!m.groups.length||m.groups.length>100||m.groups.some(v=>!Number.isSafeInteger(v)||v<=0))return fail('configGroups')
  if(m.languages.length>100||m.voices.length>100||[...m.languages,...m.voices].some(v=>!v||v.length>100))return fail('configLanguage')
  if(m.kind==='asr'&&(m.languages.length!==1||(m.provider==='volcengine'?!m.languages.includes('auto'):!['zh-CN','en-US'].includes(m.languages[0]))))return fail('configLanguage')
  if(m.provider==='aliyun'&&(m.kind==='asr'?m.region!=='cn-shanghai':m.region!=='cn-beijing'||!m.upstreamModel.trim())||m.provider==='volcengine'&&!m.resourceId.trim())return fail('configRegion')
  if(m.kind==='tts'&&!m.voices.length)return fail('configVoices')
  const before=previous.find(v=>v.id===m.id)
  const priced=(v:SpeechModel)=>JSON.stringify({...v,label:'',groups:[],enabled:false})
  if(before&&m.version===before.version&&priced(before)!==priced(m))return fail('configVersion')
 }
}
