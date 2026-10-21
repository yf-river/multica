#!/usr/bin/env python3
"""发布切换期间临时接管 Web 端口，进程随发布成功或恢复结束退出。"""
import argparse
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

PAGE='''<!doctype html><html lang="zh-CN"><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Multica 正在更新</title><style>body{font:16px system-ui;margin:12vh auto;padding:24px;max-width:560px;line-height:1.8;color:#333}h1{font-size:24px}p{color:#666}</style><h1>系统正在更新</h1><p role="status">正在切换服务，请稍候。页面会在服务恢复后自动刷新。</p><script>setInterval(async()=>{try{const r=await fetch('/api/config',{cache:'no-store'});if(r.ok)location.reload()}catch{}},2000)</script></html>'''.encode()
class Handler(BaseHTTPRequestHandler):
    def do_GET(self):
        self.send_response(503);self.send_header('Retry-After','2');self.send_header('Cache-Control','no-store')
        body=b'{"maintenance":true}' if self.path.startswith('/api/') else PAGE
        self.send_header('Content-Type','application/json' if self.path.startswith('/api/') else 'text/html; charset=utf-8');self.send_header('Content-Length',str(len(body)));self.end_headers();self.wfile.write(body)
    def log_message(self,*args):pass
if __name__=='__main__':
    p=argparse.ArgumentParser();p.add_argument('--port',required=True,type=int);a=p.parse_args();ThreadingHTTPServer(('0.0.0.0',a.port),Handler).serve_forever()
