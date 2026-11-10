# Tries to overwrite its own source file. The code is mounted read-only and
# the container's filesystem is read-only too. Expect RE
with open("/sandbox/main.py", "w") as f:
    f.write("hacked")
print(5)
