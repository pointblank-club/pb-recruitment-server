#!/usr/bin/env python3
"""Isolated capacity staircase: independent constant-rate holds, drain, then next rate."""
import argparse,json,os,pathlib,subprocess,sys,time
p=argparse.ArgumentParser();p.add_argument('manifest');p.add_argument('--rates',default='300,600,900,1200,1800,2400');p.add_argument('--hold-seconds',type=int,default=300);p.add_argument('--budget',type=int,default=18000);a=p.parse_args()
root=pathlib.Path(__file__).resolve().parent
f=json.load(open(a.manifest));
if f['environment']!='isolated' or f['api']=='https://api.recruitment.pointblank.club':raise SystemExit('Isolated target required')
if a.hold_seconds<300 or a.budget<=0:raise SystemExit('At least five minutes per hold and a positive budget are required')
rates=[int(x) for x in a.rates.split(',')];
if not(all(0<x<=10000 for x in rates) and rates==sorted(set(rates))):raise SystemExit('Use positive increasing rates')
stamp=time.strftime('%Y%m%dT%H%M%SZ',time.gmtime());results=[];spent=0
for rate in rates:
 planned=(rate*a.hold_seconds+59)//60
 if spent+planned>a.budget:break
 label=f'capacity-{stamp}-{rate}'
 env=dict(os.environ,STEP_RATE_PER_MINUTE=str(rate),DURATION_SECONDS=str(a.hold_seconds),MAX_SUBMISSIONS=str(planned))
 r=subprocess.run([sys.executable,str(root/'run.py'),'breakpoint',a.manifest,'--label',label],env=env)
 report=json.load(open(root/'results'/label/'report.json'));results.append(dict(configured_submissions_per_minute=rate,**report));spent+=report['started_posts']
 if r.returncode:break
passed=[r for r in results if r['passed']]
summary={'steps':results,'highest_passing_configured_sps':passed[-1]['configured_submissions_per_minute']/60 if passed else None,'highest_demonstrated_completion_sps':passed[-1]['observed_scored_completion_sps'] if passed else None,'planned_budget':a.budget,'actual_started_posts':spent,'not_executed_rates':rates[len(results):]}
(root/'results'/f'capacity-{stamp}.json').write_text(json.dumps(summary,indent=2));print(json.dumps(summary,indent=2))
