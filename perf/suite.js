import http from 'k6/http';
import encoding from 'k6/encoding';
import { sleep } from 'k6';
import exec from 'k6/execution';
import { SharedArray } from 'k6/data';
import { Counter, Rate, Trend } from 'k6/metrics';
import { plan } from './profiles.mjs';
import { programAt, programs } from './programs.mjs';
const cfg=plan(__ENV);
const users=new SharedArray('synthetic users',()=>JSON.parse(open(__ENV.FIXTURES)).users);
const fixture=new SharedArray('fixture metadata',()=>{const f=JSON.parse(open(__ENV.FIXTURES));delete f.users;return [f];})[0];
if(fixture.cleaned||!fixture.run_id.startsWith('pbperf-')||fixture.contest!==fixture.run_id)throw Error('Dedicated test fixture required');
if(cfg.isolated&&fixture.environment!=='isolated')throw Error('This profile requires an isolated environment');
if(users.length<cfg.identity_pool_required||new Set(users.map(u=>u.uid)).size!==users.length)throw Error('Insufficient distinct identities');
if(cfg.planned_submissions>cfg.budget)throw Error('Budget invalid');
const receipt=new Trend('receipt_latency',true), verdict=new Trend('verdict_latency',true), afterReceipt=new Trend('receipt_to_verdict',true), reads=new Trend('read_latency',true), pollCount=new Trend('polls_per_submission');
const correctness=new Rate('correct_verdict'), unexpected=new Rate('unexpected_failure');
const saved=new Counter('saved_submissions'),completed=new Counter('completed_submissions'), jobs=new Counter('completed_testcases'), unresolved=new Counter('unresolved_frontend'), authErrors=new Counter('auth_failure'), detailsErrors=new Counter('details_failure'), rejects=new Counter('admission_rejections'), attempts=new Counter('submission_attempts'), ambiguous=new Counter('ambiguous_posts'), runsCompleted=new Counter('completed_runs');
const runAttempts=new Counter('custom_run_attempts'),budgetStopped=new Counter('work_budget_exhausted'),boundaryIterations=new Counter('schedule_boundary_iterations');
const spread=new Trend('submission_start_offset',true), runtime=new Trend('reported_execution_ms',true), memory=new Trend('reported_memory_kib');
const thresholds={...cfg.thresholds};
if(cfg.profile==='expected'&&cfg.workload==='journey')for(const [i,target] of [50,100,300,500,600].entries())for(const metric of ['read_latency','receipt_latency','verdict_latency','unexpected_failure'])thresholds[metric+'{participant_stage:'+target+'}']=cfg.thresholds[metric].map(t=>({...t,delayAbortEval:(i*360+90)+'s'}));
export const options={scenarios:{[cfg.profile]:cfg.scenario},thresholds,systemTags:['status','method','name','scenario','expected_response','error_code'],summaryTrendStats:['avg','min','med','max','p(90)','p(95)','p(99)'],tags:{profile:cfg.profile,workload:cfg.workload},setupTimeout:'60s'};
let localUser, nextRefresh=0, nextRunAt=0, nextSubmitAt=0;
function stageTags(){return cfg.profile==='expected'&&cfg.workload==='journey'?{participant_stage:String([50,100,300,500,600][Math.min(4,Math.floor((Date.now()-exec.scenario.startTime)/360000))])}:{};}
function event(data){console.log(JSON.stringify({run_id:fixture.run_id,at:Date.now(),...data}));}
function user(){
 if(!localUser){const identity=users[exec.vu.idInTest-1];if(!identity)throw Error('VU lacks a distinct synthetic identity');localUser={...identity};event({event:'actor',uid:localUser.uid,scenario_start:exec.scenario.startTime});}
 if(Date.now()/1000>localUser.expires-300+(exec.vu.idInTest%120)){
  if(Date.now()<nextRefresh)exec.test.abort('Authentication refresh failed');
  nextRefresh=Date.now()+60000;
  const r=http.post('https://securetoken.googleapis.com/v1/token?key='+fixture.api_key,{grant_type:'refresh_token',refresh_token:localUser.refresh},{tags:{name:'auth_refresh',endpoint:'auth_refresh'},timeout:'15s'});
  if(r.status!==200){authErrors.add(1);exec.test.abort('Managed token refresh failed');}
  const j=r.json();localUser.token=j.id_token;localUser.refresh=j.refresh_token;localUser.expires=Date.now()/1000+Number(j.expires_in)-100;
 }
 return localUser;
}
function api(method,path,name,body,timeout='10s'){
 const u=user();const r=http.request(method,fixture.api+path,body==null?null:JSON.stringify(body),{headers:{Authorization:'Bearer '+u.token,'Content-Type':'application/json'},tags:{...stageTags(),name,endpoint:name},timeout,responseCallback:http.expectedStatuses(200,201,429)});
 const rejected=r.status===429; if(rejected)rejects.add(1,{endpoint:name,reason:name==='run'?'capacity_or_cooldown':'cooldown'});
 unexpected.add(![200,201,429].includes(r.status),{...stageTags(),endpoint:name});if([401,403].includes(r.status))authErrors.add(1);
 if(method==='GET')reads.add(r.timings.duration,{...stageTags(),endpoint:name});return r;
}
function json(r){try{return r.json();}catch(_){return {};}}
function read(path,name){const r=api('GET',path,name);if(r.status===429){const seconds=Math.min(10,Math.max(1,Number(r.headers['Retry-After']||5)));sleep(seconds);return api('GET',path,name);}return r;}
function problem(kind){const p=fixture.problems.find(p=>p.kind===kind);if(!p)throw Error('Fixture problem absent');return p;}
function browse(){
 read('/contests/'+fixture.contest,'contest');const list=json(read('/contests/'+fixture.contest+'/problems','problems'));
 // Optional conservative per-problem fan-out; current ProblemCard itself fetches no history.
 if(__ENV.HISTORY_FANOUT==='1')for(const p of fixture.problems)read('/submission/list?problem_id='+p.id+'&page=0','history');
 read('/contests/'+fixture.contest+'/leaderboard?page=0','leaderboard');
 return list;
}
function waitVerdict(id,start,received,p,prog){
 let delay=1, errors=0,polls=0;const pollStart=Date.now();
 while(Date.now()-pollStart<60000){sleep(delay);polls++;
  const r=api('GET','/submission/'+id+'/status','status');
  if(r.status!==200){if(++errors>=3)break;}else{errors=0;const status=json(r).status;if(status&&!['pending','processing'].includes(status)){
   const final=Date.now(); verdict.add(final-start,{...stageTags(),program:prog.name});afterReceipt.add(final-received,{program:prog.name});correctness.add(status===prog.expected,{program:prog.name});completed.add(1,{type:prog.kind.startsWith('mcq')?'mcq':'code'});
   const d=api('GET','/submission/'+id+'/details','details');detailsErrors.add(d.status===200?0:1);const detail=json(d),results=detail.test_case_results||[];
   if(p.cases){jobs.add(results.length);correctness.add(results.length===p.cases&&results.every(x=>x.submission_id===id));}
   if(detail.status!==status||detail.problem_id!==p.id||detail.contest_id!==fixture.contest)correctness.add(false);if(p.cases&&results.some(x=>x.status!==(status==='accepted'?'pass':status)))correctness.add(false);if(detail.runtime!=null)runtime.add(detail.runtime);if(detail.memory!=null)memory.add(detail.memory);
   pollCount.add(polls);event({event:'final',id,status,expected:prog.expected,program:prog.name,cases:results.length,start,received,final});return;
  }}
  if(r.status===429)sleep(Math.min(10,Number(r.headers['Retry-After']||5)));
  delay=Math.min(Math.round(delay*1000*1.6)/1000,5);
 }
 pollCount.add(polls);unresolved.add(1);event({event:'observe_later',id,start,received,expected:prog.expected,program:prog.name});
}
function allowed(){if(exec.scenario.iterationInTest>=cfg.budget){if(cfg.scenario.executor==='constant-arrival-rate'&&cfg.planned_iterations<=cfg.budget){boundaryIterations.add(1);return false;}budgetStopped.add(1);exec.test.abort('Maximum work budget reached; observe saved work independently');return false;}return true;}
function submit(prog){
 const p=problem(prog.kind),u=user();const start=Date.now();attempts.add(1);spread.add(start-exec.scenario.startTime);
 const marker=fixture.run_id+':'+exec.scenario.startTime+':'+exec.scenario.iterationInTest+':'+exec.vu.idInTest;
 event({event:'intent',uid:u.uid,problem:p.id,marker,start,expected:prog.expected,program:prog.name});
 const body={contest_id:fixture.contest,problem_id:p.id,type:prog.kind.startsWith('mcq')?'mcq':'code'};
 if(prog.kind.startsWith('mcq'))body.option=prog.option;else{body.language=prog.language;body.code=prog.source+'\n'+(prog.language==='python'?'#':'//')+' '.repeat(Math.min(60000,Number(__ENV.CODE_PADDING_BYTES||0)))+'\n'+(prog.language==='python'?'#':'//')+' '+marker+'\n';}
 if(body.type==='code')body.code=encoding.b64encode(body.code);
 const r=api('POST','/submission/submit','submit',body,'30s');receipt.add(r.timings.duration,stageTags());
 const id=json(r).submission_id;
 // Keep this identity occupied through the deployed five-second cooldown.
 nextSubmitAt=Date.now()+6000;
 if(r.status!==201||!id){if(r.status!==429){ambiguous.add(1);event({event:'ambiguous',uid:u.uid,problem:p.id,marker,start,http_status:r.status,expected:prog.expected});}return;}
 const received=Date.now();if(__ENV.PROBE_RUN_BUSY==='1')customRun();saved.add(1);event({event:'saved',id,uid:u.uid,problem:p.id,start,received,expected:prog.expected,program:prog.name});waitVerdict(id,start,received,p,prog);
}
export function setup(){
 const u=users[0]; const r=http.get(fixture.api+'/contests/'+fixture.contest,{headers:{Authorization:'Bearer '+u.token},tags:{name:'fixture_preflight'}});
 if(r.status!==200||r.json().name!=='[PERF TEST] '+fixture.run_id||r.json().is_registered!==true)throw Error('Fixture identity/registration preflight failed');
}
export function arrival(){if(!allowed()){sleep(1);return;}submit(programAt(exec.scenario.iterationInTest+Number(__ENV.PROGRAM_OFFSET||0),__ENV.MATRIX==='1'||cfg.profile==='smoke'));sleep(Math.max(0,(nextSubmitAt-Date.now())/1000));}
export function burst(){if(!allowed())return;sleep(Math.max(0,(exec.scenario.startTime+5000+((exec.vu.idInTest-1)/cfg.participants)*cfg.duration*1000-Date.now())/1000));submit(programAt(exec.vu.idInTest-1));}
export function runs(){if(!allowed()){sleep(1);return;}customRun();if(__ENV.PROBE_RUN_REPEAT==='1')customRun();}
function customRun(){if(Date.now()<nextRunAt)sleep((nextRunAt-Date.now())/1000);runAttempts.add(1);event({event:'run_intent',uid:user().uid,marker:fixture.run_id+' custom '+exec.vu.idInTest+' '+exec.scenario.iterationInTest});const prog=programs[0],r=api('POST','/submission/run','run',{language:prog.language,code:prog.source+'\n# '+fixture.run_id+' custom '+exec.vu.idInTest+' '+exec.scenario.iterationInTest+'\n',stdin:'1 3\n'+' '.repeat(Math.min(60000,Number(__ENV.INPUT_PADDING_BYTES||0)))},'20s');if(r.status===429)nextRunAt=Date.now()+Math.min(10,Number(r.headers['Retry-After']||5))*1000;if(r.status===200){const j=json(r);correctness.add(j.status==='accepted'&&j.stdout==='4\n',{program:'custom_run'});if(j.status==='accepted')runsCompleted.add(1);event({event:'run_final',status:j.status});}else if(r.status!==429)event({event:'run_may_continue',http_status:r.status});}
export function journey(){
 if(!allowed()){sleep(1);return;}
 browse();if(exec.vu.iterationInScenario===0)sleep((exec.vu.idInTest*137%720));
 // Preserve each participant's initial phase on later submission cycles.
 const began=Date.now();
 const prog=programAt(exec.vu.iterationInScenario+exec.vu.idInTest);read('/contests/'+fixture.contest+'/problems/'+problem(prog.kind).id,'statement');read('/submission/list?problem_id='+problem(prog.kind).id+'&page=0','history');sleep(30+(exec.vu.idInTest%31));
 if(Date.now()>=exec.scenario.startTime+cfg.duration*1000)return;
 if(exec.vu.idInTest%5===0)customRun();
 if(Date.now()>=exec.scenario.startTime+cfg.duration*1000)return;
 submit(prog);browse();
 if(__ENV.POPULATED==='1')for(let page=1;page<3;page++)read('/submission/list?problem_id='+problem('small').id+'&page='+page,'history');
 sleep(Math.max(0,720-(Date.now()-began)/1000));
}
export function mixed(){if(!allowed()){sleep(1);return;}browse();const prog=programAt(exec.scenario.iterationInTest+Number(__ENV.PROGRAM_OFFSET||0),__ENV.MATRIX==='1');read('/contests/'+fixture.contest+'/problems/'+problem(prog.kind).id,'statement');read('/submission/list?problem_id='+problem(prog.kind).id+'&page=0','history');if(__ENV.POPULATED==='1')for(let page=1;page<3;page++)read('/submission/list?problem_id='+problem('small').id+'&page='+page,'history');customRun();submit(prog);browse();sleep(Math.max(0,(nextSubmitAt-Date.now())/1000));}
export function handleSummary(data){return {[__ENV.SUMMARY||'summary.json']:JSON.stringify({plan:cfg,result:data},null,2)};}
