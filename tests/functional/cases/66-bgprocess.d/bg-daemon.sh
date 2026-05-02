#!/bin/sh
# Simulates a self-backgrounding daemon:
# 1. Starts a new session (setsid)
# 2. Writes its PID to the pid-file
# 3. Sleeps forever
echo $$ > /tmp/bg-svc.pid
exec sleep 3600
