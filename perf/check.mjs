import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';
import vm from 'node:vm';
import {spawnSync} from 'node:child_process';
import {plan,profileNames} from './profiles.mjs';
import {programs} from './programs.mjs';
for(const name of profileNames){const p=plan({PROFILE:name});assert(p.budget>0);assert(p.planned_submissions<=p.budget);assert(p.scenario.gracefulStop);assert(p.observation_seconds>0);}
assert.equal(plan({PROFILE:'smoke'}).planned_submissions,3);
assert.equal(plan({PROFILE:'baseline'}).planned_submissions,10);
assert.equal(plan({PROFILE:'soak'}).duration,7200);
assert.equal(plan({PROFILE:'deadline'}).planned_submissions,100);
for(const name of ['stress','spike','soak','breakpoint','resilience'])assert(plan({PROFILE:name}).isolated);
assert.throws(()=>plan({PROFILE:'typo'}));assert.throws(()=>plan({MAX_SUBMISSIONS:'-1'}));assert.throws(()=>plan({PARTICIPANTS:'0'}));assert.throws(()=>plan({WORKLOAD:'typo'}));
assert.equal(new Set(programs.map(p=>p.name)).size,programs.length);
assert.deepEqual(new Set(programs.map(p=>p.expected)),new Set(['accepted','wrong_answer','failed_to_process','rte','tle']));
const outputProgram=programs.find(p=>p.name==='output-volume');
const output=spawnSync('python3',['-c',outputProgram.source],{encoding:'utf8'});
assert.equal(output.status,0);assert.equal(Buffer.byteLength(output.stdout),4096);assert.notEqual(output.stdout,'4\n');
console.log('Profile arithmetic, limits, isolation, program matrix: passed');

assert.equal(plan({PROFILE:'expected',WORKLOAD:'mixed',VUS:'96'}).identity_pool_required,96);
assert.equal(plan({PROFILE:'expected',WORKLOAD:'mixed',VUS:'96'}).allocated_vus,96);

const receiptDiagnostic=plan({RECEIPT_ABORT_P95_MS:'5000'}).thresholds.receipt_latency;
assert.equal(receiptDiagnostic[0].threshold,'p(95)<3000');assert.equal(receiptDiagnostic[0].abortOnFail,false);assert.equal(receiptDiagnostic[1].threshold,'p(95)<5000');assert.equal(receiptDiagnostic[1].abortOnFail,true);
assert.equal(plan({PROFILE:'expected',MAX_SUBMISSIONS:'1000'}).planned_primary_judge_jobs_upper_bound,11000);
assert.equal(plan({PROFILE:'smoke',WORKLOAD:'runs',PROBE_RUN_REPEAT:'1'}).planned_primary_judge_jobs_upper_bound,6);
assert.equal(plan({PROFILE:'smoke',WORKLOAD:'mixed',PROBE_RUN_BUSY:'1'}).planned_primary_judge_jobs_upper_bound,66);

// Run the actual journey with a virtual clock: initial jitter must persist across cycles.
const journey=readFileSync(new URL('./suite.js',import.meta.url),'utf8').split('export function journey(){')[1].split('\nexport function mixed(){')[0];
let now=0;const submissions=[];
const actor={idInTest:2,iterationInScenario:0};
const context=vm.createContext({allowed:()=>true,Date:{now:()=>now},Math,
 exec:{vu:actor,scenario:{startTime:0}},cfg:{duration:1800},__ENV:{},fixture:{contest:'test'},
 browse:()=>{now+=60;},read:()=>{now+=20;},sleep:s=>{now+=s*1000;},
 programAt:()=>({kind:'small'}),problem:()=>({id:'test'}),customRun:()=>{},
 submit:()=>{submissions.push(now);now+=12000;}});
vm.runInContext('function journey(){'+journey,context);
vm.runInContext('journey()',context);actor.iterationInScenario++;
vm.runInContext('journey()',context);
assert(submissions[1]-submissions[0]>=720000&&submissions[1]-submissions[0]<721000);
now=1800000;actor.iterationInScenario++;
vm.runInContext('journey()',context);assert.equal(submissions.length,2);
now=1760000;actor.idInTest=5;context.customRun=()=>{now+=20000;};
vm.runInContext('journey()',context);assert.equal(submissions.length,2);
console.log('Participant pacing preserves initial phase: passed');
