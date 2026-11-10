# Asks for about 800 GB in one go. Python refuses with its own MemoryError
# before the kernel has to step in. Expect MLE
numbers = [0] * (10 ** 11)
