# Fork bomb: every process keeps making copies of itself. The 64-process
# limit makes fork() fail once 64 exist, so it can never take over your
# machine. Which limit stops it first varies: expect TLE, RE or MLE.
import os

while True:
    os.fork()
