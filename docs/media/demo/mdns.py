# Announces one device over mDNS every 20 s, as an HMI or a camera does:
# its A record and one DNS-SD service (_http._tcp), from UDP port 5353 to
# 224.0.0.251. For the demo LAN of demo-net.sh only.
#   python3 mdns.py <hostname> <ip> <instance name>
import socket, struct, sys, time

host, ip, instance = sys.argv[1], sys.argv[2], sys.argv[3]

def name(n):
    out = b''
    for label in n.rstrip('.').split('.'):
        b = label.encode()
        out += bytes([len(b)]) + b
    return out + b'\0'

def rr(owner, rtype, rdata, ttl=120, flush=True):
    cls = 0x8001 if flush else 0x0001
    return name(owner) + struct.pack('!HHIH', rtype, cls, ttl, len(rdata)) + rdata

fqdn = host + '.local'
svc = '_http._tcp.local'
inst = instance + '.' + svc
answers = [
    rr(fqdn, 1, socket.inet_aton(ip)),
    rr(svc, 12, name(inst), flush=False),
    rr(inst, 33, struct.pack('!HHH', 0, 0, 80) + name(fqdn)),
    rr(inst, 16, bytes([len(b'path=/')]) + b'path=/'),
]
msg = struct.pack('!HHHHHH', 0, 0x8400, 0, len(answers), 0, 0) + b''.join(answers)

s = socket.socket(socket.AF_INET, socket.SOCK_DGRAM, socket.IPPROTO_UDP)
s.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
s.setsockopt(socket.IPPROTO_IP, socket.IP_MULTICAST_TTL, 255)
s.bind(('', 5353))
while True:
    s.sendto(msg, ('224.0.0.251', 5353))
    time.sleep(20)
