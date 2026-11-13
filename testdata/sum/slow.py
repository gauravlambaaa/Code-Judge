# Correct, but slow: it sleeps 5 seconds before answering. scripts/crash.sh
# uses it so there's time to kill a worker in the middle of judging it.
# Expect AC if the problem's time limit is over 5 s (TLE with the usual 2 s).
import time

time.sleep(5)
a, b = map(int, input().split())
print(a + b)
