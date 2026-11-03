import json, socket, sys, os
s = socket.socket(socket.AF_UNIX); s.connect(os.path.expanduser("~/.nanoc/nanoc.sock"))
s.sendall((json.dumps({"cmd": "run", "argv": sys.argv[1:]}) + "\n").encode())
r = json.loads(s.makefile().readline())
print(r.get("output", r))
