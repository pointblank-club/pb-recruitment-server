import {programAt} from './programs.mjs';
export const profileNames=['smoke','baseline','expected','deadline','stress','spike','soak','breakpoint','volume','scalability','recovery','resilience','fairness'];
export function plan(env={}) {
 const profile=env.PROFILE||'smoke'; if(!profileNames.includes(profile)) throw Error('Unknown PROFILE');
 const isolated=['stress','spike','soak','breakpoint','volume','scalability','recovery','resilience','fairness'].includes(profile);
 const participants=Number(env.PARTICIPANTS||(['smoke','baseline'].includes(profile)?3:600));
 const vus=Number(env.VUS||participants);if(!Number.isInteger(vus)||vus<1||vus>10000)throw Error('Invalid VUS');
 const workload=env.WORKLOAD||(['expected','soak'].includes(profile)?'journey':['spike','recovery'].includes(profile)?'burst':profile==='fairness'?'mixed':'arrival');
 if(!['journey','arrival','burst','runs','mixed'].includes(workload)) throw Error('Unknown WORKLOAD');
 let duration=Number(env.DURATION_SECONDS||({smoke:120,baseline:300,expected:1800,deadline:10,stress:1200,spike:10,soak:7200,breakpoint:1800,volume:300,scalability:300,recovery:10,resilience:300,fairness:300}[profile]));
 if(duration<=0||!Number.isFinite(duration)||!Number.isInteger(participants)||participants<1||participants>1200)throw Error('Invalid duration or participants');
 let rate=Number(env.RATE_PER_MINUTE||({smoke:2,baseline:2,deadline:600,volume:6,scalability:50,resilience:6,fairness:50}[profile]||50));
 if(rate<=0||!Number.isFinite(rate))throw Error('Invalid rate');
 const budget=Number(env.MAX_SUBMISSIONS||({smoke:3,baseline:10,expected:2500,deadline:100,stress:6000,spike:600,soak:6500,breakpoint:18000,volume:30,scalability:250,recovery:600,resilience:30,fairness:300}[profile]));
 if(!Number.isSafeInteger(budget)||budget<1)throw Error('Invalid budget');
 const grace=Number(env.OBSERVATION_SECONDS||180);
 const threshold=(s)=>[{threshold:s,abortOnFail:true,delayAbortEval:env.ABORT_DELAY||'30s'}];
 let scenario={exec:workload,gracefulStop:`${grace}s`};
 let planned;
 if(profile==='smoke'&&['arrival','runs'].includes(workload)){scenario={...scenario,executor:'per-vu-iterations',vus:participants,iterations:1,maxDuration:'120s'};planned=participants;}
 else if(workload==='burst'){scenario={...scenario,executor:'per-vu-iterations',vus:participants,iterations:1,maxDuration:`${duration+90}s`};planned=participants;}
 else if(workload==='journey'){
  if(profile==='expected') scenario={...scenario,executor:'ramping-vus',startVUs:0,stages:[50,100,300,500,600].flatMap(target=>[{duration:'60s',target},{duration:'300s',target}]),gracefulRampDown:`${grace}s`};
  else scenario={...scenario,executor:'constant-vus',vus:participants,duration:`${duration}s`};
  planned=Math.ceil((profile==='expected'?540000:participants*duration)/720); // 10 submissions/person/120m, uniformly staggered.
 }else if(['stress','breakpoint'].includes(profile)&&!env.STEP_RATE_PER_MINUTE) {
  const steps=profile==='stress'?[600,900,1200,1500]:[300,600,900,1200,1800,2400];
  scenario={...scenario,executor:'ramping-arrival-rate',startRate:rate,timeUnit:'1m',preAllocatedVUs:vus,maxVUs:vus,stages:steps.map(target=>({duration:`${duration/steps.length}s`,target}))};
  planned=Math.ceil(steps.reduce((sum,x,i)=>sum+(i?steps[i-1]:rate)+x,0)*duration/steps.length/120);
 }else {if(env.STEP_RATE_PER_MINUTE)rate=Number(env.STEP_RATE_PER_MINUTE);scenario={...scenario,executor:'constant-arrival-rate',rate:Math.round(rate),timeUnit:'1m',duration:`${duration}s`,preAllocatedVUs:vus,maxVUs:vus};planned=Math.ceil(rate*duration/60);}
 if(profile==='expected'&&workload==='journey'&&duration!==1800)throw Error('Expected journey stages total 1800 seconds');
 if(profile==='smoke'&&duration!==120)throw Error('Smoke observation maximum is fixed at 120 seconds');
 if(profile==='expected'&&participants!==600)throw Error('Expected stages require 600 distinct users');
 const attempts=Math.min(budget,planned);
 const matrix=env.MATRIX==='1'||profile==='smoke';
 const caseCounts={small:1,representative:10,fanout:20,memory:1,mcq:0,mcq_wrong:0};
 let logicalJobs=0;for(let i=0;i<attempts;i++)logicalJobs+=caseCounts[programAt(i+Number(env.PROGRAM_OFFSET||0),matrix).kind];
 const meanCases=attempts?logicalJobs/attempts:0;
 const readsPerSubmission=['journey','mixed'].includes(workload)?8+(env.HISTORY_FANOUT==='1'?12:0)+(env.POPULATED==='1'?2:0):0;
 const polls=Number(env.ASSUMED_POLLS||12);
 const customRuns=(workload==='runs'?attempts*(env.PROBE_RUN_REPEAT==='1'?2:1):workload==='mixed'?attempts:workload==='journey'?Math.ceil(attempts/5):0)+(workload!=='runs'&&env.PROBE_RUN_BUSY==='1'?attempts:0);
 const httpRequests=(workload==='runs'?0:attempts*(1+polls+1+readsPerSubmission))+customRuns;
 const maxCustomPerIteration=workload==='runs'?(env.PROBE_RUN_REPEAT==='1'?2:1):(['journey','mixed'].includes(workload)?1:0)+(env.PROBE_RUN_BUSY==='1'?1:0);
 const maxCasesPerSubmission=workload==='runs'?0:matrix?20:10;

 return {profile,isolated,workload,participants,allocated_vus:['arrival','runs','mixed'].includes(workload)?vus:participants,identity_pool_required:['arrival','runs','mixed'].includes(workload)?vus:participants,duration,rate,budget,planned_iterations:planned,planned_submissions:workload==='runs'?0:attempts,planned_custom_runs:customRuns,planned_logical_judge_jobs:workload==='runs'?0:logicalJobs,planned_work_is_estimate:workload==='journey',assumed_cases_per_submission:meanCases,assumed_polls_per_submission:polls,planned_http_requests:httpRequests,planned_primary_judge_jobs_estimate:(workload==='runs'?0:logicalJobs)+customRuns,planned_primary_judge_jobs_upper_bound:budget*(maxCasesPerSubmission+maxCustomPerIteration),http_rate_assumption:'steady-state work estimate; burst polls/details extend beyond the submission window',assumed_http_requests_per_submission:workload==='runs'?null:1+polls+1+readsPerSubmission,assumed_http_rps:httpRequests/duration,assumed_scored_sps:workload==='runs'?0:attempts/duration,assumed_testcase_eps:workload==='runs'?0:attempts*meanCases/duration,observation_seconds:grace,scenario,
 thresholds:{read_latency:threshold(`p(95)<${env.READ_P95_MS||2000}`),receipt_latency:env.RECEIPT_ABORT_P95_MS?[{threshold:`p(95)<${env.RECEIPT_P95_MS||3000}`,abortOnFail:false},...threshold(`p(95)<${env.RECEIPT_ABORT_P95_MS}`)]:threshold(`p(95)<${env.RECEIPT_P95_MS||3000}`),verdict_latency:threshold(`p(95)<${env.VERDICT_P95_MS||60000}`),correct_verdict:threshold('rate==1'),unexpected_failure:threshold(`rate<${env.ERROR_RATE||0.01}`),auth_failure:threshold('count==0'),unresolved_frontend:threshold('count==0'),details_failure:threshold('count==0'),dropped_iterations:threshold('count==0'),'admission_rejections{endpoint:submit}':threshold(`count<=${env.SCORED_REJECTION_BUDGET||0}`)}};
}
