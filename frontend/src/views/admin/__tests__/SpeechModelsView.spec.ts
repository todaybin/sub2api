// Copyright (c) 2026 WorkMesh. 保留所有权利。
import {test,expect,vi} from 'vitest'
import {mount,flushPromises} from '@vue/test-utils'
import SpeechModelsView from '../SpeechModelsView.vue'
const {config,save,reconciliation,reconcile,list,getAll}=vi.hoisted(()=>({config:vi.fn(),save:vi.fn(),reconciliation:vi.fn(),reconcile:vi.fn(),list:vi.fn(),getAll:vi.fn()}))
vi.mock('@/api/speech',()=>({speechAPI:{config,save,reconciliation,reconcile}}))
vi.mock('@/api/admin/accounts',()=>({list}))
vi.mock('@/api/admin/groups',()=>({getAll}))
vi.mock('vue-router',()=>({onBeforeRouteLeave:vi.fn()}))
vi.mock('@/components/layout/AppLayout.vue',()=>({default:{template:'<slot/>'}}))
vi.mock('vue-i18n',()=>({useI18n:()=>({t:(v:string)=>v})}))
test('零价必须明确确认，保存时冻结配置，失败后保留输入',async()=>{
 config.mockResolvedValue({items:[{id:'asr',label:'识别',version:'v1',provider:'aliyun',kind:'asr',accountId:1,region:'cn-shanghai',upstreamModel:'',resourceId:'',groups:[1],languages:['zh-CN'],voices:[],usdPerUnit:0,unit:'seconds',enabled:true}],version:'config-v1'})
 reconciliation.mockResolvedValue({items:[],nextCursor:''});list.mockResolvedValue({items:[{id:1,name:'upstream'}],total:1});getAll.mockResolvedValue([{id:1,name:'points',allow_points_billing:true}])
 let fail!:(reason:unknown)=>void;save.mockImplementation(()=>new Promise((_,reject)=>{fail=reject}))
 const wrapper=mount(SpeechModelsView,{global:{stubs:{AppLayout:{template:'<slot/>'}}}})
 const button=(text:string)=>wrapper.findAll('button').find(v=>v.text()===text)!
 try{await flushPromises();await button('speech.save').trigger('click');expect(save).not.toHaveBeenCalled();expect(wrapper.text()).toContain('speech.zeroConfirm');await wrapper.findAll('input[type="checkbox"]').at(-1)!.setValue(true);await button('speech.save').trigger('click');expect(wrapper.get('fieldset').attributes()).toHaveProperty('disabled');fail({message:'SPEECH_CONFIG_CONFLICT'});await flushPromises();expect(wrapper.text()).toContain('speech.configConflict');expect((wrapper.findAll('input')[1].element as HTMLInputElement).value).toBe('asr')}finally{wrapper.unmount()}
})
