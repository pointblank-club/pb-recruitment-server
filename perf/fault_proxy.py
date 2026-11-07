#!/usr/bin/env python3
"""Isolated HTTP fault proxy. No request/response payloads or URLs are logged."""
import argparse,http.client,http.server,json,threading,time,urllib.parse
p=argparse.ArgumentParser();p.add_argument('config');args=p.parse_args()
config=json.load(open(args.config))
if config['environment']!='isolated' or not config['run_id'].startswith('pbperf-'):raise SystemExit('Isolated test run required')
for key in ['api','judge']:
 u=urllib.parse.urlsplit(config[key])
 if u.hostname not in ['127.0.0.1','localhost']:raise SystemExit('Use private isolated services only')
started=time.monotonic();lock=threading.Lock();count=0
class Handler(http.server.BaseHTTPRequestHandler):
 def log_message(self,*_):pass
 def do_GET(self):self.forward()
 def do_POST(self):self.forward()
 def do_PUT(self):self.forward()
 def forward(self):
  global count
  callback=self.path.startswith('/callback/')
  if not(callback or self.path.startswith('/judge/')):self.send_error(404);return
  active=time.monotonic()-started<float(config.get('fault_seconds',30))
  if callback and active:
   mode=config.get('callbacks','normal')
   if mode=='missing':self.send_response(200);self.end_headers();return
   if mode=='delayed':time.sleep(min(30,float(config.get('delay_seconds',10))))
  if not callback and active and self.command=='POST':
   with lock:count+=1;reject=count<=int(config.get('judge_failures',0))
   if reject:self.send_response(503);self.end_headers();return
  base=urllib.parse.urlsplit(config['api' if callback else 'judge']);path=self.path[len('/callback' if callback else '/judge'):]
  length=int(self.headers.get('Content-Length','0'))
  if length>2**20:self.send_error(413);return
  body=self.rfile.read(length);headers={k:v for k,v in self.headers.items() if k.lower() not in ['host','connection','content-length','transfer-encoding']}
  def send():
   cls=http.client.HTTPSConnection if base.scheme=='https' else http.client.HTTPConnection
   conn=cls(base.hostname,base.port,timeout=40)
   try:conn.request(self.command,base.path+path,body,headers);r=conn.getresponse();return r.status,r.read(2**21),r.getheader('Content-Type','application/json')
   finally:conn.close()
  try:
   status,data,kind=send()
   if callback and active and config.get('callbacks')=='duplicate':send()
   self.send_response(status);self.send_header('Content-Type',kind);self.send_header('Content-Length',str(len(data)));self.end_headers();self.wfile.write(data)
  except (OSError,http.client.HTTPException):self.send_error(502)
http.server.ThreadingHTTPServer(('127.0.0.1',int(config.get('port',23590))),Handler).serve_forever()
