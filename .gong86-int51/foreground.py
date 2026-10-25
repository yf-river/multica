import subprocess,threading,sys,json,time
stop=threading.Event();errors=[]
def heartbeat():
 while not stop.wait(20):
  p=subprocess.run(['multica','issue','delivery','claim-owner','01a17118-80ef-717e-b9d0-9059a78db295','--delivery','01a1723e-ac21-78a7-a120-5842f6732045','--output','json'],capture_output=True,text=True)
  if p.returncode:errors.append('owner renewal failed');return
thread=threading.Thread(target=heartbeat,daemon=True);thread.start()
try:
 result=subprocess.run(sys.argv[1:]);assert not errors,errors
finally:
 stop.set();thread.join()
raise SystemExit(result.returncode)
