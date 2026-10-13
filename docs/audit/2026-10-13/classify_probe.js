import { classify,counters } from '../../../tests/load/helpers/classify.js';
export const options={vus:1,iterations:1};
export default function(){for(let i=0;i<999;i++) classify({status:200,json:()=>({status:'PROCESSED'})}); classify({status:500}); console.log('VU processed='+counters.processed.value+' technical='+counters.technical.value);}
export function teardown(){console.log('teardown processed='+counters.processed.value+' technical='+counters.technical.value);}
