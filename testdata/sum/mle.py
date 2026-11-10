# Memory bomb: keeps grabbing 10 MB chunks until the kernel kills it at the
# memory limit. Expect MLE
chunks = []
while True:
    chunks.append(b"x" * (10 * 1024 * 1024))
