# Tries to reach the internet. With --network=none the container has no
# network interface except loopback, so this fails. Expect RE
import urllib.request

urllib.request.urlopen("http://1.1.1.1", timeout=3)
print(5)
