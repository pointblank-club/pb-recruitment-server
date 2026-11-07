import http.server,json,pathlib,socket,subprocess,tempfile,threading,time,unittest,urllib.request,urllib.error
ROOT=pathlib.Path(__file__).resolve().parent
class Upstream(http.server.BaseHTTPRequestHandler):
 events=[]
 def log_message(self,*args):pass
 def do_PUT(self):self.handle_request()
 def do_POST(self):self.handle_request()
 def handle_request(self):
  self.events.append((self.command,self.path,self.rfile.read(int(self.headers.get('Content-Length','0')))))
  self.send_response(200 if self.command=='PUT' else 201);self.end_headers();self.wfile.write(b'{}')
class FaultProxyTest(unittest.TestCase):
 def test_modes_preserve_bounded_requests(self):
  upstream=http.server.ThreadingHTTPServer(('127.0.0.1',0),Upstream);thread=threading.Thread(target=upstream.serve_forever,daemon=True);thread.start()
  self.addCleanup(upstream.server_close);self.addCleanup(upstream.shutdown)
  for mode in ['normal','missing','duplicate','delayed','transient']:
   with self.subTest(mode=mode),tempfile.TemporaryDirectory() as tmp:
    Upstream.events=[];sock=socket.socket();sock.bind(('127.0.0.1',0));port=sock.getsockname()[1];sock.close()
    url=f'http://127.0.0.1:{upstream.server_port}';config={'environment':'isolated','run_id':'pbperf-20261106T000000Z-test','api':url,'judge':url,'port':port,'callbacks':mode,'delay_seconds':0.05,'judge_failures':int(mode=='transient'),'fault_seconds':30}
    path=pathlib.Path(tmp)/'config.json';path.write_text(json.dumps(config));proc=subprocess.Popen(['python3',str(ROOT/'fault_proxy.py'),str(path)],stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)
    try:
     for _ in range(100):
      try:
       with socket.create_connection(('127.0.0.1',port),timeout=.1):break
      except OSError:time.sleep(.02)
     route='/judge/submissions' if mode=='transient' else '/callback/internal/judge0/callback/owned?sig=fake&attempt=one'
     req=urllib.request.Request(f'http://127.0.0.1:{port}'+route,data=b'{}',method='POST' if mode=='transient' else 'PUT');start=time.monotonic()
     if mode=='transient':
      with self.assertRaises(urllib.error.HTTPError) as error:urllib.request.urlopen(req)
      self.assertEqual(error.exception.code,503)
      self.assertEqual(urllib.request.urlopen(req).status,201)
     else:self.assertEqual(urllib.request.urlopen(req).status,200)
     self.assertEqual(len(Upstream.events),{'missing':0,'duplicate':2}.get(mode,1))
     if mode=='delayed':self.assertGreaterEqual(time.monotonic()-start,.05)
     if Upstream.events:self.assertEqual(Upstream.events[0][1],route.removeprefix('/judge').removeprefix('/callback'))
    finally:proc.terminate();proc.wait(timeout=5)
 def test_worker_refuses_foreign_name(self):
  p=subprocess.run(['python3',str(ROOT/'interrupt_worker.py'),'pbperf-test','real-worker'],capture_output=True);self.assertNotEqual(p.returncode,0)

class ReconciliationTest(unittest.TestCase):
 def test_ids_prevent_clock_based_misclassification(self):
  import importlib.util
  spec=importlib.util.spec_from_file_location('perf_runner',ROOT/'run.py');runner=importlib.util.module_from_spec(spec);spec.loader.exec_module(runner)
  with tempfile.TemporaryDirectory() as tmp:
   out=pathlib.Path(tmp)
   (out/'before-ids.json').write_text('["old"]')
   events=[{'event':'intent','start':12000},{'event':'saved','id':'new','expected':'accepted','received':13000},{'event':'final','id':'new'}]
   (out/'correlation.jsonl').write_text('\n'.join(map(json.dumps,events)))
   (out/'backlog.jsonl').write_text(json.dumps({'at':'1970-01-01T00:00:14Z','pending_submissions':0,'oldest_pending_seconds':0}))
   subs=[{'id':'old','status':'accepted','type':'code','created':0},{'id':'new','status':'accepted','type':'code','created':0}]
   plan={'profile':'smoke','workload':'arrival','planned_submissions':1};before={'terminal_executions':0};after={'terminal_executions':1,'pending_submissions':0,'score_mismatches':0,'passed':True}
   report=runner.analyze(out,plan,subs,10,15,before,after,0)
   self.assertTrue(report['passed']);self.assertEqual(report['database_new_submissions'],1)
   subs.append({'id':'unknown','status':'accepted','type':'code','created':0})
   report=runner.analyze(out,plan,subs,10,15,before,after,0)
   self.assertFalse(report['passed']);self.assertEqual(report['unmatched_saved_work'],1)
   (out/'correlation.jsonl').write_text('')
   report=runner.analyze(out,plan,subs[:1],10,15,before,after,0)
   self.assertFalse(report['passed']);self.assertFalse(report['work_observation_complete'])


class K6SuiteTest(unittest.TestCase):
 def test_smoke_against_local_contract_stub(self):
  # This validates the harness/encoding only; it executes no submitted program.
  import base64,os
  run='pbperf-20261106T000000Z-contract'
  class API(http.server.BaseHTTPRequestHandler):
   body=None
   def log_message(self,*args):pass
   def do_GET(self):
    if self.path.endswith('/status'):result={'status':'accepted'}
    elif self.path.endswith('/details'):result={'id':'owned','contest_id':run,'problem_id':'small','status':'accepted','test_case_results':[{'submission_id':'owned','status':'pass'}]}
    else:result={'name':'[PERF TEST] '+run,'is_registered':True}
    self.send_response(200);self.end_headers();self.wfile.write(json.dumps(result).encode())
   def do_POST(self):
    API.body=json.loads(self.rfile.read(int(self.headers['Content-Length'])));self.send_response(201);self.end_headers();self.wfile.write(b'{"submission_id":"owned"}')
  server=http.server.ThreadingHTTPServer(('127.0.0.1',0),API);threading.Thread(target=server.serve_forever,daemon=True).start();self.addCleanup(server.server_close);self.addCleanup(server.shutdown)
  with tempfile.TemporaryDirectory() as tmp:
   path=pathlib.Path(tmp);fixture={'run_id':run,'contest':run,'api':f'http://127.0.0.1:{server.server_port}','environment':'isolated','users':[{'uid':run+'-u0000','token':'fake','expires':9999999999}],'problems':[{'id':'small','kind':'small','cases':1}]};(path/'fixture.json').write_text(json.dumps(fixture))
   env=dict(os.environ,PROFILE='smoke',PARTICIPANTS='1',FIXTURES=str(path/'fixture.json'),SUMMARY=str(path/'summary.json'))
   began=time.monotonic();proc=subprocess.run([str(pathlib.Path.home()/'.local/bin/k6'),'run','--quiet',str(ROOT/'suite.js')],env=env,capture_output=True,text=True,timeout=20)
   self.assertEqual(proc.returncode,0,proc.stderr);self.assertGreaterEqual(time.monotonic()-began,5);self.assertEqual(API.body['contest_id'],run);self.assertIn(run,base64.b64decode(API.body['code']).decode());self.assertEqual(json.loads((path/'summary.json').read_text())['result']['metrics']['correct_verdict']['values']['rate'],1)


class InterruptedPostTest(unittest.TestCase):
 def test_recovery_requires_source_and_ownership(self):
  import base64,importlib.util
  spec=importlib.util.spec_from_file_location('post_reconciler',ROOT/'reconcile_posts.py');module=importlib.util.module_from_spec(spec);spec.loader.exec_module(module)
  fixture={'contest':'own','problems':[{'id':'p','cases':1}]};row={'id':'s','uid':'u','problem':'p'};intent={'marker':'unique','expected':'accepted'}
  details={'id':'s','user_id':'u','contest_id':'own','problem_id':'p','status':'accepted','code':base64.b64encode(b'print(4)\n# unique\n').decode(),'test_case_results':[{'submission_id':'s','status':'pass'}]}
  module.verify(details,row,intent,fixture)
  with self.assertRaises(ValueError):module.verify(dict(details,user_id='foreign'),row,intent,fixture)
  with self.assertRaises(ValueError):module.verify(dict(details,code=base64.b64encode(b'print(4)\n# different\n').decode()),row,intent,fixture)

if __name__=='__main__':unittest.main()
