#!/usr/bin/env python3
"""Local Docker only: pause an explicitly labelled isolated worker, then always resume."""
import argparse,json,subprocess,time
p=argparse.ArgumentParser();p.add_argument('run_id');p.add_argument('container');p.add_argument('--seconds',type=int,default=15);a=p.parse_args()
if not(a.run_id.startswith('pbperf-') and a.container.startswith(a.run_id+'-') and 1<=a.seconds<=30):raise SystemExit('A named, bounded, test-owned interruption is required')
info=json.loads(subprocess.check_output(['docker','inspect',a.container]))[0]
labels=info['Config'].get('Labels') or {}
if labels.get('pbperf.run_id')!=a.run_id or labels.get('pbperf.environment')!='isolated':raise SystemExit('Container must carry exact isolated-run labels')
try:
 subprocess.run(['docker','pause',a.container],check=True);time.sleep(a.seconds)
finally:subprocess.run(['docker','unpause',a.container],check=True)
