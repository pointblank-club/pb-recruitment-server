#!/usr/bin/env python3
"""Reconcile interrupted POSTs using exact owned IDs and stored source markers; no POST retries."""
import argparse,base64,json,os,pathlib,subprocess,urllib.request

def verify(details,row,intent,fixture):
 if details.get('id')!=row['id'] or details.get('user_id')!=row['uid'] or details.get('contest_id')!=fixture['contest'] or details.get('problem_id')!=row['problem']:raise ValueError('Ownership mismatch')
 source=base64.b64decode(details['code'],validate=True).decode('utf-8')
 if not any('\n'+prefix+intent['marker']+'\n' in source for prefix in ['# ','// ']):raise ValueError('Source marker mismatch')
 cases=next(p['cases'] for p in fixture['problems'] if p['id']==row['problem']);results=details.get('test_case_results') or []
 if details.get('status')!=intent['expected'] or len(results)!=cases or any(r['submission_id']!=row['id'] or r['status']!=('pass' if intent['expected']=='accepted' else intent['expected']) for r in results):raise ValueError('Final outcome mismatch')

def main():
 os.umask(0o077);p=argparse.ArgumentParser();p.add_argument('manifest',type=pathlib.Path);p.add_argument('result',type=pathlib.Path);a=p.parse_args();f=json.loads(a.manifest.read_text());root=pathlib.Path(__file__).parent
 rows=json.loads((a.result/'reconciliation.json').read_text());before=set(json.loads((a.result/'before-ids.json').read_text()));events=[]
 for line in (a.result/'correlation.jsonl').read_text().splitlines():
  try:events.append(json.loads(line))
  except ValueError:pass
 intents=[e for e in events if e.get('event')=='intent'];saved=[e for e in events if e.get('event')=='saved'];acked={(e['uid'],e['start']) for e in saved};known={e['id'] for e in saved};pending=[e for e in intents if (e['uid'],e['start']) not in acked];recovered=[];unverified=[]
 users={u['uid']:u for u in f['users']}
 for row in rows:
  if row['id'] in before or row['id'] in known:continue
  candidates=[i for i in pending if i['uid']==row['uid'] and i['problem']==row['problem']]
  if row['uid'] not in users or len(candidates)!=1:unverified.append(row['id']);continue
  try:
   req=urllib.request.Request(f['api']+'/submission/'+row['id']+'/details',headers={'Authorization':'Bearer '+users[row['uid']]['token']})
   with urllib.request.urlopen(req,timeout=15) as response:details=json.load(response)
   verify(details,row,candidates[0],f);recovered.append({'id':row['id'],'uid':row['uid'],'status':row['status'],'marker':candidates[0]['marker'],'program':candidates[0].get('program','unknown')})
  except Exception:unverified.append(row['id'])
 new=[r for r in rows if r['id'] not in before]
 result={'recovered':recovered,'unverified_saved_ids':unverified,'unacknowledged_intents':len(pending),'unacknowledged_intents_without_new_saved_row':len(pending)-len(recovered),'saved_submissions':len(new),'pending_submissions':sum(r['status']=='pending' for r in new),'all_saved_final':all(r['status']!='pending' for r in new)}
 (a.result/'post-reconciliation.json').write_text(json.dumps(result,indent=2));print(json.dumps({k:v for k,v in result.items() if k not in ['recovered','unverified_saved_ids']}|{'recovered_submissions':len(recovered),'unverified_saved_submissions':len(unverified)},indent=2))
 return 0 if not unverified and result['all_saved_final'] else 1
if __name__=='__main__':raise SystemExit(main())
