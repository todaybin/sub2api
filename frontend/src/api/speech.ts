// Copyright (c) 2026 WorkMesh. 保留所有权利。
import {apiClient} from './client'
export interface SpeechModel {
 id:string;label:string;kind:'asr'|'tts';provider:'aliyun'|'volcengine';accountId:number
 upstreamModel:string;resourceId:string;region:string;groups:number[];languages:string[];voices:string[]
 usdPerUnit:number|null;unit:'seconds'|'characters';version:string;enabled:boolean
}
export interface SpeechBill {id:string;model:string;feature:string;priceVersion:string;state:string;createdAt:string;quantity:number;points:number;pending:number;uncertain?:number;billingState?:string}
export interface SpeechReconciliationRow {id:string;sessionId:string;providerRequestId:string;provider:string;model:string;quantity:number;unit:string;updatedAt:string}
export const speechAPI={
 async config(){return (await apiClient.get<{items:SpeechModel[];version:string}>('/admin/speech/models')).data},
 async save(items:SpeechModel[],expectedVersion:string){return (await apiClient.put<{version:string}>('/admin/speech/models',{items,expectedVersion})).data},
 async reconciliation(cursor=''){return (await apiClient.get<{items:SpeechReconciliationRow[];nextCursor:string}>('/admin/speech/reconciliation',{params:{cursor}})).data},
 async reconcile(id:string,input:{decision:'settled'|'released';quantity:number;evidence:string}){return (await apiClient.post('/admin/speech/meters/'+encodeURIComponent(id)+'/reconcile',input)).data},
 async bills(cursor=''){return (await apiClient.get<{items:SpeechBill[];nextCursor:string}>('/user/speech-history',{params:{cursor}})).data}
}
