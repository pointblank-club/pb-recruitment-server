// Separate isolated bottleneck probe; never used for production end-to-end figures.
import http from 'k6/http';
import {sleep} from 'k6';
import {Counter,Rate,Trend} from 'k6/metrics';
import {programs} from './programs.mjs';
const f=JSON.parse(open(__ENV.FIXTURES));
if(f.environment!=='isolated'||f.api==='https://api.recruitment.pointblank.club'||!/^http:\/\/(127\.0\.0\.1|localhost):\d+$/.test(__ENV.PRIVATE_JUDGE||''))throw Error('An isolated judge over existing private loopback access is required');
const done=new Counter('direct_completed_executions'),correct=new Rate('direct_correct'),latency=new Trend('direct_completion_ms',true),unresolved=new Counter('direct_unresolved');
export const options={scenarios:{direct:{executor:'constant-arrival-rate',rate:Number(__ENV.RATE_PER_MINUTE||6),timeUnit:'1m',duration:__ENV.DURATION||'2m',preAllocatedVUs:3,maxVUs:3,gracefulStop:'60s'}},thresholds:{direct_correct:[{threshold:'rate==1',abortOnFail:true}],direct_unresolved:['count==0'],dropped_iterations:['count==0']},systemTags:['status','method','name','scenario']};
export default function(){
 if(__ITER>=Number(__ENV.MAX_JOBS_PER_VU||10))return;
 const start=Date.now(),r=http.post(__ENV.PRIVATE_JUDGE+'/submissions?base64_encoded=false&wait=false',JSON.stringify({source_code:programs[0].source,language_id:71,stdin:'1 3\n',expected_output:'4\n',cpu_time_limit:1,memory_limit:262144}),{headers:{'Content-Type':'application/json'},tags:{name:'direct_create'},timeout:'15s'});
 if(r.status!==201){correct.add(false);return;}
 const token=r.json('token');
 while(Date.now()-start<60000){sleep(1);const s=http.get(__ENV.PRIVATE_JUDGE+'/submissions/'+token,{tags:{name:'direct_status'},timeout:'5s'});if(s.status!==200)continue;const status=s.json('status.id');if(status>2){correct.add(status===3);done.add(1);latency.add(Date.now()-start);return;}}
 unresolved.add(1);
}
