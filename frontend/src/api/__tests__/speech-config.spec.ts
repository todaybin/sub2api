// Copyright (c) 2026 WorkMesh. 保留所有权利。
import {describe,it,expect} from 'vitest'
import {validateSpeechModels} from '../speech-config'
import type {SpeechModel} from '../speech'
const model=():SpeechModel=>({id:'asr',label:'识别',version:'v1',provider:'aliyun',kind:'asr',accountId:1,region:'cn-shanghai',upstreamModel:'',resourceId:'',groups:[1],languages:['zh-CN'],voices:[],usdPerUnit:0,unit:'seconds',enabled:true})
describe('语音目录配置',()=>{
 it('允许显式免费但拒绝缺价和错误语言',()=>{const m=model();expect(validateSpeechModels([m],[])).toBeUndefined();m.usdPerUnit=null;expect(validateSpeechModels([m],[])?.key).toBe('configPrice');m.usdPerUnit=1;m.languages=['zh-CN','en-US'];expect(validateSpeechModels([m],[])?.key).toBe('configLanguage')})
 it('价格和供应商能力变更必须更新版本',()=>{const before=model(),m={...before,usdPerUnit:1};expect(validateSpeechModels([m],[before])?.key).toBe('configVersion');m.version='v2';expect(validateSpeechModels([m],[before])).toBeUndefined()})
 it('字节只允许当前接口真实自动识别能力',()=>{const m={...model(),provider:'volcengine' as const,resourceId:'resource'};expect(validateSpeechModels([m],[])?.key).toBe('configLanguage');m.languages=['auto'];expect(validateSpeechModels([m],[])).toBeUndefined()})
})
