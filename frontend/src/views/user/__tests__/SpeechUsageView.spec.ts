// Copyright (c) 2026 WorkMesh. 保留所有权利。
import {test,expect,vi} from 'vitest'
import {mount,flushPromises} from '@vue/test-utils'
import SpeechUsageView from '../SpeechUsageView.vue'
const {bills}=vi.hoisted(()=>({bills:vi.fn()}))
vi.mock('@/api/speech',()=>({speechAPI:{bills}}))
vi.mock('@/components/layout/AppLayout.vue',()=>({default:{template:'<slot/>'}}))
vi.mock('vue-i18n',()=>({useI18n:()=>({t:(v:string)=>v})}))
test('首屏失败后可以重试，未知用量显示预扣保留',async()=>{
 bills.mockRejectedValueOnce(Error('offline')).mockResolvedValueOnce({items:[{id:'one',model:'tts',priceVersion:'v1',feature:'tts',createdAt:new Date().toISOString(),quantity:0,points:0,state:'interrupted',pending:1,uncertain:1}],nextCursor:''})
 const wrapper=mount(SpeechUsageView,{global:{stubs:{AppLayout:{template:'<slot/>'}}}})
 try{await flushPromises();expect(wrapper.text()).toContain('speech.loadFailed');await wrapper.get('button').trigger('click');await flushPromises();expect(bills).toHaveBeenCalledTimes(2);expect(wrapper.text()).toContain('speech.uncertain')}finally{wrapper.unmount()}
})
