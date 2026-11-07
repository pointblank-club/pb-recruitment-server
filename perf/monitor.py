#!/usr/bin/env python3
"""Read-only remote sampler. Run via SSH stdin; never prints env, logs, or job data."""
import datetime,json,os,subprocess,time,sys
NAMES=['recruitment-server','recruitment-ingresss','judge0-v1131_server_1','judge0-v1131_workers_1','judge0-v1131_redis_1','judge0-v1131_db_1']
def cpu_stat_path(cgroups):
 v2=None
 for row in cgroups.splitlines():
  _,controllers,path=row.split(':',2)
  if 'cpu' in controllers.split(','):return '/sys/fs/cgroup/cpu'+path+'/cpu.stat'
  if not controllers:v2='/sys/fs/cgroup'+path+'/cpu.stat'
 if v2:return v2
 raise ValueError('CPU cgroup unavailable')
assert cpu_stat_path('2:cpu,cpuacct:/docker/test\n0::/docker/test')=='/sys/fs/cgroup/cpu/docker/test/cpu.stat'
assert cpu_stat_path('0::/system.slice/test')=='/sys/fs/cgroup/system.slice/test/cpu.stat'
def call(args):
 p=subprocess.run(args,capture_output=True,text=True,timeout=12)
 return p.stdout.strip() if p.returncode==0 else None
for _ in range(int(sys.argv[1]) if len(sys.argv)>1 else 720):
 try:
  record={'at':datetime.datetime.now(datetime.timezone.utc).isoformat(),'load':os.getloadavg()}
  for name,path in [('cpu','/proc/stat'),('memory','/proc/meminfo'),('disk','/proc/diskstats'),('network','/proc/net/dev'),('vm','/proc/vmstat'),('sockets','/proc/net/sockstat')]:
   with open(path) as f: record[name]=f.read()
  stats=call(['docker','stats','--no-stream','--format','{{json .}}',*NAMES]);record['containers']=[json.loads(x) for x in stats.splitlines()] if stats else None
  info=call(['docker','inspect','--format','{{json .Name}} {{.State.Pid}} {{.RestartCount}} {{.State.OOMKilled}} {{.HostConfig.NanoCpus}} {{.HostConfig.Memory}}',*NAMES]);record['container_state']=info
  record['throttling']={}
  if info:
   for line in info.splitlines():
    name,pid,*_=line.split()
    name=json.loads(name)
    try:
     record['throttling'][name]=open(cpu_stat_path(open('/proc/'+pid+'/cgroup').read())).read()
    except (OSError,IndexError,ValueError): pass
  workers=call(['docker','exec','judge0-v1131_server_1','curl','--max-time','3','-fsS','http://localhost:2358/workers'])
  record['judge_workers']=json.loads(workers) if workers else None
  record['judge_pending']=call(['docker','exec','judge0-v1131_db_1','sh','-c','psql -U "$POSTGRES_USER" -d "$POSTGRES_DB" -Atc "SELECT count(*) FILTER(WHERE status_id=1),count(*) FILTER(WHERE status_id=2),COALESCE(max(extract(epoch from now()-created_at)),0),COALESCE(max(extract(epoch from now()-created_at)) FILTER(WHERE status_id=1),0) FROM submissions WHERE status_id IN (1,2)"'])
  record['judge_db']=call(['docker','exec','judge0-v1131_db_1','sh','-c','psql -U "$POSTGRES_USER" -d "$POSTGRES_DB" -Atc "SELECT count(*),count(*) FILTER(WHERE state=\'active\') FROM pg_stat_activity"'])
  record['redis_ping']=call(['docker','exec','judge0-v1131_redis_1','sh','-c','REDISCLI_AUTH="$REDIS_PASSWORD" redis-cli ping'])
  print(json.dumps(record),flush=True)
 except Exception as exc: print(json.dumps({'at':datetime.datetime.now(datetime.timezone.utc).isoformat(),'sampler_error':type(exc).__name__}),flush=True)
 time.sleep(10)
