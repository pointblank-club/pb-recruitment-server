#!/usr/bin/env python3
"""Read-only Judge0 inventory. Raw tokens stay in an owner-only correlation file."""
import argparse,base64,datetime,json,os,pathlib,re,subprocess
p=argparse.ArgumentParser();p.add_argument('manifest',type=pathlib.Path);p.add_argument('--legacy-custom-windows',action='store_true');p.add_argument('--legacy-result',action='append',default=[]);a=p.parse_args()
os.umask(0o077);m=json.loads(a.manifest.read_text());assert re.fullmatch(r'pbperf-\d{8}T\d{6}Z-[a-z0-9]+',m['run_id'])
executions=json.loads((a.manifest.parent/(m['run_id']+'-executions.json')).read_text());tokens=[e['judge0_token'] for e in executions if e['judge0_token']];assert all(re.fullmatch(r'[a-zA-Z0-9_-]+',t) for t in tokens)
def quote(s):return "'"+s.replace("'","''")+"'"
predicates=['token IN ('+','.join(map(quote,tokens))+')'] if tokens else []
for logfile in (pathlib.Path(__file__).parent/'results').glob('*/correlation.jsonl'):
 for line in logfile.read_text().splitlines():
  try: ev=json.loads(line)
  except ValueError:continue
  if ev.get('event')=='run_intent' and ev.get('run_id')==m['run_id']:
   source='a,b=map(int,input().split()); print(a+b)'+'\n# '+ev['marker']+'\n'
   predicates.append("decode(source_code,'base64')=decode("+quote(base64.b64encode(source.encode()).decode())+",'base64')")
windows=[]
if a.legacy_custom_windows:
 for file in (pathlib.Path(__file__).parent/'results').glob('*/metrics.jsonl'):
  correlation=file.parent/'correlation.jsonl'
  if not correlation.exists() or (m['run_id'] not in correlation.read_text() and file.parent.name not in a.legacy_result):continue
  for line in file.read_text().splitlines():
   d=json.loads(line)
   if d.get('type')=='Point' and d.get('metric')=='http_req_duration' and d['data'].get('tags',{}).get('name')=='run' and d['data']['tags'].get('status')=='200':
    end=datetime.datetime.fromisoformat(d['data']['time'].replace('Z','+00:00'));start=end-datetime.timedelta(milliseconds=d['data']['value']);windows.append((start-datetime.timedelta(seconds=1),end+datetime.timedelta(seconds=1)))
 for start,end in windows:
  predicates.append("(decode(source_code,'base64')=decode("+quote(base64.b64encode(b'a,b=map(int,input().split()); print(a+b)').decode())+",'base64') AND decode(stdin,'base64')=decode("+quote(base64.b64encode(b'1 3\n').decode())+",'base64')"+' AND created_at BETWEEN '+quote(start.isoformat())+'::timestamptz AND '+quote(end.isoformat())+'::timestamptz)')
sql="SELECT COALESCE(json_agg(json_build_object('token',token,'status_id',status_id,'created_at',created_at,'runtime_seconds',time,'memory_kib',memory)), '[]'::json) FROM submissions WHERE "+(' OR '.join(predicates) if predicates else 'false')+';'
script='import subprocess,sys\np=subprocess.run(["docker","exec","-i","judge0-v1131_db_1","sh","-c",\'psql -U "$POSTGRES_USER" -d "$POSTGRES_DB" -At\'],input='+repr(sql)+',capture_output=True,text=True)\nif p.returncode: sys.exit(1)\nsys.stdout.write(p.stdout)\n'
proc=subprocess.run(['ssh','-o','BatchMode=yes','-o','ConnectTimeout=10','-p','6622','ubuntu@13.205.163.28','sudo -n python3 -'],input=script,capture_output=True,text=True,timeout=30)
if proc.returncode:raise SystemExit('Read-only inventory failed; diagnostics suppressed')
native=json.loads(proc.stdout);(a.manifest.parent/(m['run_id']+'-native-inventory.json')).write_text(json.dumps({'run_id':m['run_id'],'jobs':native,'legacy_custom_request_windows':len(windows),'legacy_custom_correlation':'Known source/stdin and successful request windows; early custom runs lacked a source run-ID marker. No deletion authorized from time correlation alone.'},indent=2))
matched=sum(j['token'] in tokens for j in native);print(json.dumps({'scored_job_tokens':len(tokens),'native_scored_matches':matched,'additional_native_custom_matches':len(native)-matched,'native_pending':sum(j['status_id']<=2 for j in native),'legacy_request_windows':len(windows)},indent=2))
