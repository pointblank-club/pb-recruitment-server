#!/usr/bin/env python3
"""Validate every profile/workload with k6 itself; performs no network requests."""
import json,os,pathlib,subprocess,tempfile
root=pathlib.Path(__file__).resolve().parent
with tempfile.TemporaryDirectory() as tmp:
 path=pathlib.Path(tmp)/'fake.json';path.write_text(json.dumps({'environment':'isolated','api':'http://127.0.0.1:18080','run_id':'pbperf-20261106T000000Z-validate','contest':'pbperf-20261106T000000Z-validate','users':[{'uid':str(i),'token':'fake','expires':9999999999} for i in range(600)],'problems':[]}))
 profiles=['smoke','baseline','expected','deadline','stress','spike','soak','breakpoint','volume','scalability','recovery','resilience','fairness']
 workloads=['journey','arrival','burst','runs','mixed']
 checked=[]
 for profile,workload in [(x,None) for x in profiles]+[('expected',x) for x in workloads]:
  env=dict(os.environ,FIXTURES=str(path),PROFILE=profile)
  if workload:env['WORKLOAD']=workload
  proc=subprocess.run([os.environ.get('K6',str(pathlib.Path.home()/'.local/bin/k6')),'inspect','--include-system-env-vars','--execution-requirements',str(root/'suite.js')],env=env,capture_output=True,text=True)
  if proc.returncode:raise SystemExit(proc.stderr)
  checked.append({'profile':profile,'workload':workload or 'default','valid':True})
 env=dict(os.environ,FIXTURES=str(path),PRIVATE_JUDGE='http://127.0.0.1:12358')
 proc=subprocess.run([os.environ.get('K6',str(pathlib.Path.home()/'.local/bin/k6')),'inspect','--include-system-env-vars','--execution-requirements',str(root/'direct-judge.js')],env=env,capture_output=True,text=True)
 if proc.returncode:raise SystemExit(proc.stderr)
 checked.append({'profile':'direct-judge','workload':'isolated direct execution','valid':True})
 print(json.dumps({'k6':subprocess.check_output([str(pathlib.Path.home()/'.local/bin/k6'),'version'],text=True).strip(),'checks':checked},indent=2))
