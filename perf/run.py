#!/usr/bin/env python3
"""Bounded run, protected correlation, and independent observation even after a k6 abort."""
import argparse,datetime,hashlib,json,os,pathlib,subprocess,sys,time
ROOT=pathlib.Path(__file__).resolve().parent
SERVER=ROOT.parent

def invoke_fixture(command,manifest):
 timeout=max(40,60+2*len(json.loads(manifest.read_text())['users'])) if command=='refresh' else 40
 p=subprocess.run([str(ROOT/'.private/fixtures'),command,str(manifest)],cwd=SERVER,capture_output=True,text=True,timeout=timeout)
 if p.returncode: raise RuntimeError('Fixture '+command+' failed; diagnostics suppressed')
 return json.loads(p.stdout) if p.stdout.startswith('{') else p.stdout

def main():
 os.umask(0o077)
 p=argparse.ArgumentParser();p.add_argument('profile');p.add_argument('manifest',type=pathlib.Path);p.add_argument('--workload');p.add_argument('--label');p.add_argument('--no-remote-monitor',action='store_true');args=p.parse_args()
 manifest=args.manifest.resolve();f=json.loads(manifest.read_text())
 if f.get('cleaned'): raise SystemExit('Fixture already cleaned')
 label=args.label or args.profile+'-'+datetime.datetime.now(datetime.timezone.utc).strftime('%Y%m%dT%H%M%SZ')
 if not all(x.isalnum() or x in '-_' for x in label):raise SystemExit('Invalid result label')
 out=ROOT/'results'/label;out.mkdir(parents=True,exist_ok=False)
 (out/'harness-sha256.json').write_text(json.dumps({str(p.relative_to(ROOT)):hashlib.sha256(p.read_bytes()).hexdigest() for p in ROOT.rglob('*') if p.is_file() and p.suffix in ['.js','.mjs','.py','.go'] and not any(x in p.parts for x in ['.private','results','__pycache__'])},indent=2))
 (out/'run-metadata.json').write_text(json.dumps({'run_id':f['run_id'],'environment':f['environment'],'created_at':datetime.datetime.now(datetime.timezone.utc).isoformat()}))
 env=dict(os.environ,PROFILE=args.profile,FIXTURES=str(manifest),SUMMARY=str(out/'summary.json'))
 if args.workload:env['WORKLOAD']=args.workload
 config=subprocess.run(['node','--input-type=module','-e',"import('./perf/profiles.mjs').then(m=>console.log(JSON.stringify(m.plan(process.env))))"],env=env,cwd=SERVER,capture_output=True,text=True,check=True)
 plan=json.loads(config.stdout);(out/'plan.json').write_text(json.dumps(plan,indent=2))
 if plan['isolated'] and (f['environment']!='isolated' or f['api']=='https://api.recruitment.pointblank.club'):raise SystemExit('Profile requires a separate isolated judge/API')
 if f['environment']=='production' and args.profile not in ['smoke','baseline','expected','deadline']:raise SystemExit('Production profile denied')
 if len(f['users'])<plan['identity_pool_required']:raise SystemExit('Not enough distinct synthetic users')
 # Provisioning and initial token refresh are deliberately outside measurement.
 if min(u['expires'] for u in f['users'])<time.time()+900:
  invoke_fixture('refresh',manifest)
 invoke_fixture('reconcile',manifest)
 existing=json.loads((manifest.parent/(f['run_id']+'-reconciliation.json')).read_text()) or []
 (out/'before-ids.json').write_text(json.dumps([s['id'] for s in existing]))
 before=invoke_fixture('snapshot',manifest);(out/'before.json').write_text(json.dumps(before))
 if before['pending_submissions']:raise SystemExit('Earlier test work has not drained')
 snapshots=open(out/'backlog.jsonl','w');generator=open(out/'generator.jsonl','w');remote=None
 if not args.no_remote_monitor:
  remote_log=open(out/'host.jsonl','w')
  remote=subprocess.Popen(['ssh','-o','BatchMode=yes','-o','ConnectTimeout=10','-p','6622','ubuntu@13.205.163.28','sudo -n python3 - '+str(int((plan['duration']+plan['observation_seconds']+120)/10))],stdin=open(ROOT/'monitor.py'),stdout=remote_log,stderr=subprocess.DEVNULL)
 k6=os.environ.get('K6',str(pathlib.Path.home()/'.local/bin/k6'))
 inspect=subprocess.run([k6,'inspect','--include-system-env-vars','--execution-requirements',str(ROOT/'suite.js')],env=env,capture_output=True,text=True)
 (out/'validated-options.json').write_text(inspect.stdout)
 if inspect.returncode:raise RuntimeError('k6 option validation failed: '+inspect.stderr)
 started=time.time();cpu0=os.times();logs=open(out/'console.txt','w')
 proc=subprocess.Popen([k6,'run','--quiet','--out','json='+str(out/'metrics.jsonl'),'--log-format','raw','--log-output','file='+str(out/'correlation.jsonl'),str(ROOT/'suite.js')],env=env,cwd=SERVER,stdout=logs,stderr=logs)
 try:
  while proc.poll() is None:
   now=time.time()
   if now-started>plan['duration']+plan['observation_seconds']+120:proc.terminate()
   sample_generator(proc.pid,generator)
   snapshot=invoke_fixture('snapshot',manifest);snapshots.write(json.dumps(snapshot)+'\n');snapshots.flush()
   if snapshot['oldest_pending_seconds']>float(env.get('MAX_PENDING_AGE_SECONDS','90')):proc.terminate()
   time.sleep(10)
  code=proc.wait();generation_end=time.time();drain_deadline=generation_end+plan['observation_seconds'];last=None
  posted=[];receipts=[]
  for line in (out/'correlation.jsonl').read_text().splitlines():
   try:event=json.loads(line)
   except ValueError:continue
   if event.get('event')=='intent':posted.append(event)
   if event.get('event')=='saved':receipts.append(event)
  # An interrupted POST may still create work even when the first DB sample is empty.
  settle_until=drain_deadline if len(posted)>len(receipts) else generation_end
  while True:
   last=invoke_fixture('snapshot',manifest);snapshots.write(json.dumps(last)+'\n');snapshots.flush()
   if time.time()>=drain_deadline or (last['pending_submissions']==0 and time.time()>=settle_until):break
   time.sleep(5)
  invoke_fixture('reconcile',manifest)
  # Reconciliation includes ambiguous POSTs found in the exact test contest, even after generator termination.
  reconciliation=json.loads((manifest.parent/(f['run_id']+'-reconciliation.json')).read_text())
  known={u['uid'] for u in f['users']}
  if any(s['uid'] not in known for s in reconciliation):raise RuntimeError('Foreign user in test contest')
  (out/'reconciliation.json').write_text(json.dumps(reconciliation,indent=2))
  after=json.loads((manifest.parent/(f['run_id']+'-database-summary.json')).read_text());(out/'after.json').write_text(json.dumps(after,indent=2))
  report=analyze(out,plan,reconciliation,started,generation_end,before,after,code)
  (out/'report.json').write_text(json.dumps(report,indent=2));print(json.dumps(report,indent=2))
  return 0 if report['passed'] else 1
 finally:
  if proc.poll() is None:proc.terminate();proc.wait(timeout=15)
  if remote:remote.terminate();remote.wait(timeout=15);remote_log.close()
  snapshots.close();generator.close();logs.close()

def sample_generator(pid,file):
 try:
  status=pathlib.Path('/proc',str(pid),'status').read_text();stat=pathlib.Path('/proc',str(pid),'stat').read_text();sock=pathlib.Path('/proc',str(pid),'net/sockstat').read_text()
  file.write(json.dumps({'at':time.time(),'status':status,'stat':stat,'sockets':sock,'load':os.getloadavg(),'fds':len(list(pathlib.Path('/proc',str(pid),'fd').iterdir()))})+'\n');file.flush()
 except FileNotFoundError:pass

def analyze(out,plan,subs,start,end,before,after,code):
 events=[]
 for line in (out/'correlation.jsonl').read_text().splitlines():
  try:events.append(json.loads(line))
  except ValueError:pass
 saved=[e for e in events if e.get('event')=='saved'];finals=[e for e in events if e.get('event')=='final'];intents=[e for e in events if e.get('event')=='intent'];ambiguous=[e for e in events if e.get('event')=='ambiguous']
 expected={e['id']:e['expected'] for e in saved};actual={s['id']:s for s in subs};missing=[i for i in expected if i not in actual];wrong=[i for i,v in expected.items() if i in actual and actual[i]['status']!=v]
 before_ids=set(json.loads((out/'before-ids.json').read_text())) if (out/'before-ids.json').exists() else {s['id'] for s in subs if s['created']<int(start)}
 unknown=[s for s in subs if s['id'] not in before_ids and s['id'] not in expected]
 # Unmatched work remains an explicit failure; source markers can be examined through protected details.
 starts=[x['start'] for x in intents];new=[s for s in subs if s['id'] not in before_ids]
 samples=[json.loads(s) for s in (out/'backlog.jsonl').read_text().splitlines()]
 last_receipt=max([s['received']/1000 for s in saved],default=start)
 cleared=[datetime.datetime.fromisoformat(s['at'].replace('Z','+00:00')).timestamp() for s in samples if s['pending_submissions']==0 and datetime.datetime.fromisoformat(s['at'].replace('Z','+00:00')).timestamp()>=last_receipt]
 drain_upper=min(cleared)-last_receipt if cleared else None
 recovery_ok=after['pending_submissions']==0 and drain_upper is not None and drain_upper<=float(os.environ.get('QUEUE_RECOVERY_SECONDS','120'))
 metrics=json.loads((out/'summary.json').read_text())['result']['metrics'] if (out/'summary.json').exists() else {}
 values=lambda name:metrics.get(name,{}).get('values',{})
 admitted_runs=values('completed_runs').get('count',0)
 work_complete=admitted_runs>0 if plan['workload']=='runs' else bool(saved) and len(intents)==len(saved)==len(finals)
 soft_receipt={t['threshold'] for t in plan.get('thresholds',{}).get('receipt_latency',[]) if not t.get('abortOnFail',False)}
 failed_thresholds=[{'metric':name,'threshold':expression} for name,m in metrics.items() for expression,result in m.get('thresholds',{}).items() if not result.get('ok',False)]
 hard_failures=[f for f in failed_thresholds if not(f['metric'].startswith('receipt_latency') and f['threshold'] in soft_receipt)]
 actors_seen={e.get('uid') for e in events if e.get('uid')}
 journey_target_met=len(actors_seen)==plan['participants'] if plan['profile']=='expected' and plan['workload']=='journey' else None
 health_ok=code in (0,99) and not hard_failures and after['passed'] and recovery_ok and work_complete and not(missing or wrong or unknown or ambiguous) and journey_target_met is not False
 stages=[]
 if plan['profile']=='expected' and plan['workload']=='journey':
  for target in [50,100,300,500,600]:
   measured={n:values(n+'{participant_stage:'+str(target)+'}') for n in ['read_latency','receipt_latency','verdict_latency','unexpected_failure']}
   stages.append({'target_participants':target,'metrics':measured})

 return {'profile':plan['profile'],'workload':plan['workload'],'participant_stages':stages,'work_observation_complete':work_complete,'health_guards_passed':health_ok,'failed_thresholds':failed_thresholds,'hard_threshold_failures':hard_failures,'observed_distinct_actors':len(actors_seen),'journey_participant_target_met':journey_target_met,'observed_distinct_submitters':len({e.get('uid') for e in saved if e.get('uid')}),'stopped_for_work_budget':values('work_budget_exhausted').get('count',0)>0,'schedule_boundary_iterations':values('schedule_boundary_iterations').get('count',0),'dropped_iterations':values('dropped_iterations').get('count',0),'admission_rejections':values('admission_rejections').get('count',0),'custom_run_attempts':values('custom_run_attempts').get('count',0),'successful_custom_executions':admitted_runs,'oldest_test_pending_seconds':max([s['oldest_pending_seconds'] for s in samples],default=0),'test_backlog_drain_upper_bound_seconds':drain_upper,'queue_recovery_passed':recovery_ok,'receipt_p95_ms':values('receipt_latency').get('p(95)'),'verdict_p95_ms':values('verdict_latency').get('p(95)'),'read_p95_ms':values('read_latency').get('p(95)'),'k6_exit_code':code,'measurement_seconds':round(end-start,3),'planned_submissions':plan['planned_submissions'],'started_posts':len(intents),'receipt_ids':len(saved),'observed_finals':len(finals),'database_new_submissions':len(new),'database_terminal_testcases_delta':after['terminal_executions']-before['terminal_executions'],'observed_scored_completion_sps':sum(s['type']=='code' and s['status']!='pending' for s in new)/(time.time()-start),'logical_testcase_completion_eps':(after['terminal_executions']-before['terminal_executions'])/(time.time()-start),'submission_start_spread_ms':max(starts)-min(starts) if starts else None,'ambiguous_posts':len(ambiguous),'unacknowledged_post_intents':max(0,len(intents)-len(saved)-len(ambiguous)-values('admission_rejections{endpoint:submit}').get('count',0)),'unmatched_saved_work':len(unknown),'missing_saved_ids':len(missing),'unexpected_verdicts':len(wrong),'unresolved_at_observation_deadline':after['pending_submissions'],'score_mismatches':after['score_mismatches'],'observation_drain_seconds':max(0,time.time()-end),'completion_observation_seconds':time.time()-start,'passed':code==0 and after['passed'] and recovery_ok and work_complete and journey_target_met is not False and not(missing or wrong or unknown or ambiguous)}
if __name__=='__main__':sys.exit(main())
