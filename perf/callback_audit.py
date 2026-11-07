#!/usr/bin/env python3
"""Read-only callback audit. Emit counts only; never return signed URLs or raw logs."""
import argparse,datetime,json,pathlib,subprocess
p=argparse.ArgumentParser();p.add_argument('manifest',type=pathlib.Path);p.add_argument('result',type=pathlib.Path);a=p.parse_args()
f=json.loads(a.manifest.read_text());jobs=json.loads((a.manifest.parent/(f['run_id']+'-executions.json')).read_text())
# Use this measurement's saved IDs, excluding older work in the same fixture pool.
saved=set();program_by_submission={}
for line in (a.result/'correlation.jsonl').read_text().splitlines():
 try:e=json.loads(line)
 except ValueError:continue
 if e.get('event')=='saved':saved.add(e['id']);program_by_submission[e['id']]=e.get('program','unknown')
recovery=a.result/'post-reconciliation.json'
if recovery.exists():
 intents={}
 for line in (a.result/'correlation.jsonl').read_text().splitlines():
  try:event=json.loads(line)
  except ValueError:continue
  if event.get('event')=='intent':intents[event['marker']]=event.get('program','unknown')
 for entry in json.loads(recovery.read_text()).get('recovered',[]):
  saved.add(entry['id']);program_by_submission[entry['id']]=entry.get('program',intents.get(entry['marker'],'recovered'))
ids=[j['id'] for j in jobs if j['submission'] in saved]
started=json.loads((a.result/'run-metadata.json').read_text())['created_at']
settings={'ids':ids,'since':started,'programs':{j['id']:program_by_submission[j['submission']] for j in jobs if j['submission'] in saved}}
script='''import collections,json,re,subprocess
from urllib.parse import urlsplit
cfg=SETTINGS
owned=set(cfg['ids']);status=collections.Counter();by_id=collections.defaultdict(set);latencies=[];leaks={};errors=collections.Counter();by_program=collections.defaultdict(collections.Counter)
for container in ['recruitment-server','recruitment-ingresss','judge0-v1131_server_1','judge0-v1131_workers_1']:
 p=subprocess.run(['docker','logs','--since',cfg['since'],container],capture_output=True,text=True,timeout=25)
 raw=p.stdout+p.stderr
 probe=raw.replace(chr(92)+'u0026','&').replace(chr(92)+'u003f','?').replace('&amp;','&')
 leaks[container]=bool(re.search(r'(?:[?&]|%3[fF]|%26)sig=(?!\\[FILTERED\\]|%5[Bb]FILTERED)[A-Za-z0-9_-]{16,}',probe))
 if container!='recruitment-server':continue
 for line in raw.splitlines():
  try:row=json.loads(line)
  except ValueError:continue
  uri=str(row.get('uri',''));match=re.search(r'/internal/judge0/callback/([0-9a-fA-F-]{36})',uri)
  if not match or match.group(1) not in owned:continue
  code=str(row.get('status'));status[code]+=1;by_id[match.group(1)].add(code);by_program[cfg['programs'][match.group(1)]][code]+=1
  if isinstance(row.get('latency'),(float,int)):latencies.append(row['latency']/1e6)
 for line in raw.splitlines():
  if 'judge0 callback failed' not in line:continue
  if not any(i in line for i in owned):continue
  for category in ['context canceled','context deadline exceeded','deadlock','conflicting Judge0 token','SQLSTATE']:
   if category in line:errors[category]+=1;break
  else:errors['unclassified']+=1
latencies.sort()
print(json.dumps({'expected_execution_callbacks':len(owned),'callback_requests':sum(status.values()),'callback_statuses':dict(status),'callback_statuses_by_program':dict(by_program),'executions_with_successful_callback':sum('200' in v for v in by_id.values()),'executions_without_observed_successful_callback':len(owned)-sum('200' in v for v in by_id.values()),'callback_attempt_latency_max_ms':max(latencies,default=None),'callback_attempt_latency_p95_ms':latencies[min(len(latencies)-1,int((len(latencies)-1)*.95))] if latencies else None,'callback_error_categories':dict(errors),'signature_shaped_values_in_logs':leaks}))
'''.replace('SETTINGS',repr(settings))
r=subprocess.run(['ssh','-o','BatchMode=yes','-o','ConnectTimeout=10','-p','6622','ubuntu@13.205.163.28','sudo -n python3 -'],input=script,capture_output=True,text=True,timeout=120)
if r.returncode:raise SystemExit('Read-only callback audit failed; raw diagnostics suppressed')
audit=json.loads(r.stdout);(a.result/'callback-audit.json').write_text(json.dumps(audit,indent=2));print(json.dumps(audit,indent=2))
