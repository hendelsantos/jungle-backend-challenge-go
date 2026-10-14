// Exact classifier oracle: every operation contributes one denominator sample.
import { check } from 'k6';
import { classify, noteTechnical } from '../helpers/classify.js';
export const options = { vus:1, iterations:1, thresholds:{
 checks:['rate==1'], auxiliary_technical_error_rate:['rate==0.5'], auxiliary_technical_errors:['count==1'],
 financial_technical_error_rate:['rate==0.002'],
 financial_ops_processed:['count==992'], financial_ops_replays:['count==1'],
 financial_ops_rejected:['count==1'], financial_ops_conflicts:['count==1'],
 financial_ops_contract_rejections:['count==3'], technical_errors:['count==2'],
 request_timeouts:['count==1'],
}};
export default function () {
 noteTechnical({status:200}); noteTechnical({status:500});
 for(let i=0;i<992;i++) classify({status:200,json:()=>({status:'PROCESSED'})});
 for(const [status,want] of [[400,'contract'],[401,'contract'],[403,'contract'],[409,'conflict'],[422,'rejected'],[500,'technical']]) {
  check(classify({status,json:()=>({})}),{[`${status} classified`]:v=>v===want});
 }
 check(classify({status:200,json:()=>({idempotentReplay:true})}),{'replay':v=>v==='replay'});
 check(classify({status:0,error:'request timeout'}),{'timeout':v=>v==='technical'});
}
